package pluginbundle

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func writeBundle(t *testing.T, root, id, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"` + id + `","name":"Test Plugin","version":"` + version + `","author":"Tester","dll":"test.dll","ui":"ui"}`
	if err := os.WriteFile(filepath.Join(root, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "test.dll"), []byte("not a real pe file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ui", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportFolderBundle(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.0.0")

	result := store.Import(source, false)
	if result.Action != ActionAdded {
		t.Fatalf("first import = %s (%s), want added", result.Action, result.Message)
	}
	if result.ID != "example.plugin" {
		t.Fatalf("id = %q", result.ID)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "example.plugin", "ui", "index.html")); err != nil {
		t.Fatalf("UI was not installed: %v", err)
	}

	source = filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.2.0")
	result = store.Import(source, false)
	if result.Action != ActionUpdated {
		t.Fatalf("newer import = %s (%s), want updated", result.Action, result.Message)
	}

	source = filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.1.0")
	result = store.Import(source, false)
	if result.Action != ActionUnchanged {
		t.Fatalf("older import = %s (%s), want unchanged", result.Action, result.Message)
	}
}

func TestImportDLLWithSidecarUI(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(t.TempDir(), "Legacy Mod")
	if err := os.MkdirAll(filepath.Join(sourceDir, "Legacy Mod.ui"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Legacy Mod.dll"), []byte("dll"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "Legacy Mod.ui", "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := store.Import(filepath.Join(sourceDir, "Legacy Mod.dll"), false)
	if result.Action != ActionAdded {
		t.Fatalf("import = %s (%s), want added", result.Action, result.Message)
	}
	if result.UIPath == "" {
		t.Fatal("sidecar UI was not associated with the DLL")
	}
}

func TestZipSlipRejected(t *testing.T) {
	destination := t.TempDir()
	zipPath := filepath.Join(t.TempDir(), "evil.zip")
	file, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("../escape.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("nope")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := extractZip(zipPath, destination); err == nil {
		t.Fatal("expected zip slip to be rejected")
	}
}

func TestSafeRelative(t *testing.T) {
	for _, unsafe := range []string{"../escape", `C:\escape`, "C:/escape", `\\server\share`, "/etc/passwd"} {
		if _, err := safeRelative(unsafe); err == nil {
			t.Fatalf("expected %q to be rejected", unsafe)
		}
	}
	if got, err := safeRelative("ui/index.html"); err != nil || got != "ui/index.html" {
		t.Fatalf("safeRelative = %q, %v", got, err)
	}
}
