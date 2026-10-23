//go:build windows

package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateINIImportsSettingsAndDropsOverlay(t *testing.T) {
	legacy := `
; old settings
[SkyLoader]
GamePath=C:\Games\Sky\Sky.exe
BootstrapPath=C:\Games\Sky\SkyBootstrap.dll

[DLLs]
Count=3
DLL0=C:\Mods\Example.dll
DLLEnabled0=1
DLL1=C:\Mods\SkyOverlay.dll
DLLEnabled1=1
DLL2=C:\Mods\Disabled.dll
DLLEnabled2=0
`
	cfg, err := MigrateINI(legacy, `C:\SkyLoader`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(cfg.GamePath), "sky.exe") {
		t.Fatalf("GamePath = %q", cfg.GamePath)
	}
	if !strings.Contains(strings.ToLower(cfg.BootstrapPath), "skybootstrap.dll") {
		t.Fatalf("BootstrapPath = %q", cfg.BootstrapPath)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("plugins = %+v", cfg.Plugins)
	}
	for _, plugin := range cfg.Plugins {
		if strings.EqualFold(windowsBaseName(plugin.Path), "SkyOverlay.dll") {
			t.Fatalf("SkyOverlay was not dropped: %+v", plugin)
		}
	}
	if cfg.Plugins[0].Enabled != true || cfg.Plugins[1].Enabled != false {
		t.Fatalf("enabled state was not preserved: %+v", cfg.Plugins)
	}
}

func TestLoadImportsLegacyINITOnce(t *testing.T) {
	localAppData := t.TempDir()
	t.Setenv("LOCALAPPDATA", localAppData)
	executableDir := t.TempDir()

	gamePath := filepath.Join(t.TempDir(), "Sky.exe")
	bootstrapPath := filepath.Join(executableDir, "SkyBootstrap.dll")
	pluginPath := filepath.Join(t.TempDir(), "Example.dll")
	for _, file := range []string{gamePath, bootstrapPath, pluginPath} {
		if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	legacy := "[SkyLoader]\nGamePath=" + gamePath + "\nBootstrapPath=" + bootstrapPath +
		"\n[DLLs]\nCount=1\nDLL0=" + pluginPath + "\nDLLEnabled0=1\n"
	if err := os.WriteFile(filepath.Join(executableDir, legacyININame), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, status, err := LoadConfig(executableDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "Imported") {
		t.Fatalf("status = %q", status)
	}
	if len(cfg.Plugins) != 1 || !cfg.Plugins[0].Enabled {
		t.Fatalf("plugins = %+v", cfg.Plugins)
	}
	if cfg.GamePath != gamePath || cfg.BootstrapPath != bootstrapPath {
		t.Fatalf("paths = %q, %q", cfg.GamePath, cfg.BootstrapPath)
	}

	configPath := ConfigPath(executableDir)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
	var stored Config
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("config.json is invalid: %v", err)
	}

	// Changing the legacy file must not affect an existing config.json.
	if err := os.WriteFile(filepath.Join(executableDir, legacyININame), []byte("[DLLs]\nCount=0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, status, err := LoadConfig(executableDir)
	if err != nil {
		t.Fatal(err)
	}
	if status != "" {
		t.Fatalf("second load imported legacy settings again: %q", status)
	}
	if len(reloaded.Plugins) != 1 {
		t.Fatalf("second load plugins = %+v", reloaded.Plugins)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	original := Config{
		GamePath:      `C:\Games\Sky.exe`,
		BootstrapPath: `C:\Sky\SkyBootstrap.dll`,
		AutoInject:    true,
		Plugins: []PluginEntry{
			{Path: `C:\Sky\plugins\example\example.dll`, Enabled: true},
		},
	}
	if err := original.Save(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.GamePath != original.GamePath || len(loaded.Plugins) != 1 || !loaded.Plugins[0].Enabled {
		t.Fatalf("round trip = %+v", loaded)
	}
}
