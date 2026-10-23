//go:build windows

package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportTogglePersistAndRemove(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	exeDir := t.TempDir()

	source := filepath.Join(t.TempDir(), "Example.Plugin")
	if err := os.MkdirAll(filepath.Join(source, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "plugin.json"),
		`{"id":"example.plugin","name":"Example Plugin","version":"1.0.0","author":"Tester","dll":"example.dll","ui":"ui"}`)
	writeFile(t, filepath.Join(source, "example.dll"), "not a real PE file")
	writeFile(t, filepath.Join(source, "ui", "index.html"), "<html><body>hi</body></html>")

	first, err := New(exeDir, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	report := first.Import([]string{source}, false)
	if len(report.Items) != 1 || report.Items[0].Action != "added" {
		t.Fatalf("import report = %+v", report)
	}
	state := first.State()
	if len(state.Plugins) != 1 {
		t.Fatalf("plugins = %+v", state.Plugins)
	}
	plugin := state.Plugins[0]
	if plugin.ID != "example.plugin" || !plugin.HasUI || !plugin.Managed || !plugin.Enabled {
		t.Fatalf("plugin view = %+v", plugin)
	}

	if url, err := first.PluginURL("example.plugin"); err != nil || url == "" {
		t.Fatalf("PluginURL = %q, %v", url, err)
	}
	if _, ok := first.PluginUI("example.plugin"); !ok {
		t.Fatal("PluginUI did not resolve the bundle")
	}
	if err := first.SetPluginEnabled("example.plugin", false); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(exeDir, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	state = second.State()
	if len(state.Plugins) != 1 || state.Plugins[0].Enabled {
		t.Fatalf("persisted plugin state = %+v", state.Plugins)
	}

	if err := second.RemovePlugin("example.plugin"); err != nil {
		t.Fatal(err)
	}
	if len(second.State().Plugins) != 0 {
		t.Fatalf("plugin was not removed: %+v", second.State().Plugins)
	}
	if _, err := os.Stat(filepath.Join(PluginDir(exeDir), "example.plugin")); !os.IsNotExist(err) {
		t.Fatalf("managed plugin directory still exists: %v", err)
	}
}

func TestRemoveBundleWithNestedDLL(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	exeDir := t.TempDir()

	source := filepath.Join(t.TempDir(), "Nested.Plugin")
	writeFile(t, filepath.Join(source, "plugin.json"),
		`{"id":"nested.plugin","name":"Nested Plugin","version":"1.0.0","dll":"bin/nested.dll"}`)
	writeFile(t, filepath.Join(source, "bin", "nested.dll"), "not a real PE file")

	core, err := New(exeDir, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	report := core.Import([]string{source}, false)
	if len(report.Items) != 1 || report.Items[0].Action != "added" {
		t.Fatalf("import report = %+v", report)
	}
	bundleRoot := filepath.Join(PluginDir(exeDir), "nested.plugin")
	if folder, err := core.PluginFolder("nested.plugin"); err != nil || folder != bundleRoot {
		t.Fatalf("PluginFolder = %q, %v; want %q", folder, err, bundleRoot)
	}
	if err := core.RemovePlugin("nested.plugin"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bundleRoot); !os.IsNotExist(err) {
		t.Fatalf("bundle directory still exists: %v", err)
	}
	if len(core.State().Plugins) != 0 {
		t.Fatalf("plugin was not unregistered: %+v", core.State().Plugins)
	}
}

func TestSetGamePath(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	exeDir := t.TempDir()
	core, err := New(exeDir, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	game := filepath.Join(t.TempDir(), "Sky.exe")
	writeFile(t, game, "game")
	if err := core.SetGamePath(game); err != nil {
		t.Fatal(err)
	}
	if got := core.State().GamePath; got != filepath.Clean(game) {
		t.Fatalf("GamePath = %q, want %q", got, filepath.Clean(game))
	}
}

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
	if !cfg.Plugins[0].Enabled || cfg.Plugins[1].Enabled {
		t.Fatalf("enabled state was not preserved: %+v", cfg.Plugins)
	}
}

func TestLoadImportsLegacyINITOnce(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	executableDir := t.TempDir()

	gamePath := filepath.Join(t.TempDir(), "Sky.exe")
	bootstrapPath := filepath.Join(executableDir, "SkyBootstrap.dll")
	pluginPath := filepath.Join(t.TempDir(), "Example.dll")
	for _, file := range []string{gamePath, bootstrapPath, pluginPath} {
		writeFile(t, file, "x")
	}

	legacy := "[SkyLoader]\nGamePath=" + gamePath + "\nBootstrapPath=" + bootstrapPath +
		"\n[DLLs]\nCount=1\nDLL0=" + pluginPath + "\nDLLEnabled0=1\n"
	writeFile(t, filepath.Join(executableDir, legacyININame), legacy)

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

	data, err := os.ReadFile(ConfigPath(executableDir))
	if err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
	var stored Config
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("config.json is invalid: %v", err)
	}

	// Changing the legacy file must not affect an existing config.json.
	writeFile(t, filepath.Join(executableDir, legacyININame), "[DLLs]\nCount=0\n")
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
