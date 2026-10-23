//go:build windows

// Settings schema and persistence: where SkyLoader's configuration lives,
// how it is read, migrated from the legacy INI, and written back.
package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PluginEntry is one registered plugin. Paths are absolute after loading.
type PluginEntry struct {
	Path    string `json:"path"`
	UIPath  string `json:"uiPath,omitempty"`
	Enabled bool   `json:"enabled"`
}

// Config is the launcher configuration stored as JSON.
type Config struct {
	GamePath      string        `json:"gamePath"`
	BootstrapPath string        `json:"bootstrapPath"`
	AutoInject    bool          `json:"autoInject"`
	Plugins       []PluginEntry `json:"plugins"`
}

const (
	appDirectoryName   = "SkyLoader"
	configFileName     = "config.json"
	legacyININame      = "SkyLoader.ini"
	defaultGamePath    = `C:\Program Files (x86)\Steam\steamapps\common\Sky Children of the Light\Sky.exe`
	defaultBootstrap   = "SkyBootstrap.dll"
	legacyPluginFolder = "plugins"
)

// AppDir returns %LOCALAPPDATA%\SkyLoader, falling back to the executable
// directory on unusual hosts.
func AppDir(exeDir string) string {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, appDirectoryName)
	}
	if configDir, err := os.UserConfigDir(); err == nil && configDir != "" {
		return filepath.Join(configDir, appDirectoryName)
	}
	return filepath.Join(exeDir, appDirectoryName)
}

func PluginDir(exeDir string) string  { return filepath.Join(AppDir(exeDir), legacyPluginFolder) }
func UpdatesDir(exeDir string) string { return filepath.Join(AppDir(exeDir), "updates") }
func LogPath(exeDir string) string    { return filepath.Join(AppDir(exeDir), "logs", "SkyLoader.log") }
func ConfigPath(exeDir string) string { return filepath.Join(AppDir(exeDir), configFileName) }

// Defaults returns a configuration with sane paths for a fresh install.
func Defaults(exeDir string) Config {
	return Config{
		BootstrapPath: filepath.Join(exeDir, defaultBootstrap),
		AutoInject:    true,
	}
}

// LoadConfig reads config.json, or migrates the legacy SkyLoader.ini once. The
// returned status is a human-readable migration/detection message.
func LoadConfig(exeDir string) (Config, string, error) {
	appDir := AppDir(exeDir)
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		return Defaults(exeDir), "", err
	}

	path := ConfigPath(exeDir)
	cfg, status, err := readStoredConfig(path, exeDir)
	if err != nil {
		return cfg, "", err
	}

	status = cfg.applyBuiltinPaths(exeDir, status)
	cfg.sanitize(exeDir)

	// A fresh install is persisted at once so the next start skips migration.
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if err := cfg.Save(path); err != nil {
			return cfg, status, err
		}
	}
	return cfg, status, nil
}

// readStoredConfig reads config.json, importing the legacy SkyLoader.ini once
// when no configuration file exists yet.
func readStoredConfig(path, exeDir string) (Config, string, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		cfg := Defaults(exeDir)
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Defaults(exeDir), "", fmt.Errorf("parse %s: %w", path, err)
		}
		return cfg, "", nil
	case errors.Is(err, os.ErrNotExist):
		cfg, status := configFromLegacyINI(exeDir)
		return cfg, status, nil
	default:
		return Defaults(exeDir), "", err
	}
}

// configFromLegacyINI imports the old SkyLoader.ini next to the executable,
// leaving the defaults in place when there is nothing to migrate.
func configFromLegacyINI(exeDir string) (Config, string) {
	legacy, err := os.ReadFile(filepath.Join(exeDir, legacyININame))
	if err != nil {
		return Defaults(exeDir), ""
	}
	if migrated, migrationErr := MigrateINI(string(legacy), exeDir); migrationErr == nil {
		return migrated, "Imported settings from SkyLoader.ini."
	}
	return Defaults(exeDir), ""
}

// applyBuiltinPaths fills missing paths with built-in defaults, auto-detecting
// Sky.exe in a Steam library, and enriches the status message.
func (c *Config) applyBuiltinPaths(exeDir, status string) string {
	// An empty or missing bootstrap falls back to the one shipped beside the
	// executable; an empty path takes that default even if the file is absent.
	bundled := filepath.Join(exeDir, defaultBootstrap)
	if c.BootstrapPath == "" || (!isRegularFile(c.BootstrapPath) && isRegularFile(bundled)) {
		c.BootstrapPath = bundled
	}
	if c.GamePath == "" || !isRegularFile(c.GamePath) {
		if detected := detectSkyPath(); detected != "" {
			c.GamePath = detected
			if status == "" {
				status = "Auto-detected Sky.exe in a Steam library."
			}
		} else if c.GamePath == "" {
			c.GamePath = defaultGamePath
		}
	}
	return status
}

// sanitize normalizes registered plugin paths, resolves them against the
// executable directory, and drops blanks, duplicates, and the retired
// SkyOverlay.dll.
func (c *Config) sanitize(exeDir string) {
	if c.Plugins == nil {
		c.Plugins = []PluginEntry{}
	}
	seen := make(map[string]bool, len(c.Plugins))
	clean := make([]PluginEntry, 0, len(c.Plugins))
	for _, entry := range c.Plugins {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(exeDir, path)
		}
		path = normalizePath(path)
		if strings.EqualFold(windowsBaseName(path), "SkyOverlay.dll") {
			continue
		}
		key := strings.ToLower(path)
		if seen[key] {
			continue
		}
		seen[key] = true
		clean = append(clean, PluginEntry{
			Path:    path,
			UIPath:  strings.TrimSpace(entry.UIPath),
			Enabled: entry.Enabled,
		})
	}
	c.Plugins = clean
}

// Save writes the configuration atomically.
func (c Config) Save(path string) error {
	data, err := encodeJSON(c)
	if err != nil {
		return err
	}
	return writeAtomic(".config-*.json", path, data)
}

// ParseINI parses the subset of the Windows INI format used by SkyLoader.
func ParseINI(data string) map[string]map[string]string {
	sections := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			current = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			if sections[current] == nil {
				sections[current] = map[string]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || current == "" {
			continue
		}
		sections[current][strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return sections
}

// MigrateINI converts a legacy SkyLoader.ini document.
func MigrateINI(data, exeDir string) (Config, error) {
	sections := ParseINI(data)
	cfg := Defaults(exeDir)

	if section := sections["skyloader"]; section != nil {
		if value := section["gamepath"]; value != "" {
			cfg.GamePath = value
		}
		if value := section["bootstrappath"]; value != "" {
			cfg.BootstrapPath = value
		}
	}

	dlls := sections["dlls"]
	if dlls == nil {
		return cfg, nil
	}
	count, err := strconv.Atoi(dlls["count"])
	if err != nil || count < 0 {
		count = 0
	}
	if count > 512 {
		count = 512
	}
	for i := 0; i < count; i++ {
		path := dlls["dll"+strconv.Itoa(i)]
		if path == "" || strings.EqualFold(windowsBaseName(path), "SkyOverlay.dll") {
			continue
		}
		enabled := dlls["dllenabled"+strconv.Itoa(i)] != "0"
		cfg.Plugins = append(cfg.Plugins, PluginEntry{Path: path, Enabled: enabled})
	}
	cfg.sanitize(exeDir)
	return cfg, nil
}
