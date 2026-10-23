//go:build windows

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

	cfg := Defaults(exeDir)
	status := ""
	path := ConfigPath(exeDir)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return Defaults(exeDir), "", fmt.Errorf("parse %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
		legacy := filepath.Join(exeDir, legacyININame)
		if legacyData, legacyErr := os.ReadFile(legacy); legacyErr == nil {
			if migrated, migrationErr := MigrateINI(string(legacyData), exeDir); migrationErr == nil {
				cfg = migrated
				status = "Imported settings from SkyLoader.ini."
			}
		}
	default:
		return Defaults(exeDir), "", err
	}

	if cfg.BootstrapPath == "" {
		cfg.BootstrapPath = filepath.Join(exeDir, defaultBootstrap)
	}
	if !isRegularFile(cfg.BootstrapPath) {
		if fallback := filepath.Join(exeDir, defaultBootstrap); isRegularFile(fallback) {
			cfg.BootstrapPath = fallback
		}
	}
	if cfg.GamePath == "" || !isRegularFile(cfg.GamePath) {
		if detected := detectSkyPath(); detected != "" {
			cfg.GamePath = detected
			if status == "" {
				status = "Auto-detected Sky.exe in a Steam library."
			}
		} else if cfg.GamePath == "" {
			cfg.GamePath = defaultGamePath
		}
	}
	cfg.sanitize(exeDir)

	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		if err := cfg.Save(path); err != nil {
			return cfg, status, err
		}
	}
	return cfg, status, nil
}

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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
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
	cfg.AutoInject = true

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
