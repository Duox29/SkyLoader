//go:build windows

package launcher

import (
	"os"
	"path/filepath"
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

	url, err := first.PluginURL("example.plugin")
	if err != nil || url == "" {
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
	if _, err := os.Stat(filepath.Join(second.store.Root, "example.plugin")); !os.IsNotExist(err) {
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
	bundleRoot := filepath.Join(core.store.Root, "nested.plugin")
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
	launcher, err := New(exeDir, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer launcher.Close()

	game := filepath.Join(t.TempDir(), "Sky.exe")
	writeFile(t, game, "game")
	if err := launcher.SetGamePath(game); err != nil {
		t.Fatal(err)
	}
	if got := launcher.State().GamePath; got != filepath.Clean(game) {
		t.Fatalf("GamePath = %q, want %q", got, filepath.Clean(game))
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
