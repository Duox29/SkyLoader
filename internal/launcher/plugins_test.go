//go:build windows

package launcher

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBundle(t *testing.T, root, id, version string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "plugin.json"),
		`{"id":"`+id+`","name":"Test Plugin","version":"`+version+`","author":"Tester","dll":"test.dll","ui":"ui"}`)
	writeFile(t, filepath.Join(root, "test.dll"), "not a real pe file")
	writeFile(t, filepath.Join(root, "ui", "index.html"), "<html></html>")
}

func TestImportFolderBundle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.0.0")

	result := importPlugin(root, source, false)
	if result.Action != ActionAdded || result.ID != "example.plugin" {
		t.Fatalf("first import = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "example.plugin", "ui", "index.html")); err != nil {
		t.Fatalf("UI was not installed: %v", err)
	}

	source = filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.2.0")
	if result = importPlugin(root, source, false); result.Action != ActionUpdated {
		t.Fatalf("newer import = %+v, want updated", result)
	}

	source = filepath.Join(t.TempDir(), "Example.Plugin")
	writeBundle(t, source, "example.plugin", "1.1.0")
	if result = importPlugin(root, source, false); result.Action != ActionUnchanged {
		t.Fatalf("older import = %+v, want unchanged", result)
	}
}

func TestImportDLLWithSidecarUI(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(t.TempDir(), "Legacy Mod")
	writeFile(t, filepath.Join(sourceDir, "Legacy Mod.dll"), "dll")
	writeFile(t, filepath.Join(sourceDir, "Legacy Mod.ui", "index.html"), "<html></html>")
	source := filepath.Join(sourceDir, "Legacy Mod.dll")

	result := importPlugin(root, source, false)
	if result.Action != ActionAdded {
		t.Fatalf("import = %+v, want added", result)
	}
	bundle, err := loadBundle(filepath.Join(root, DirectoryName(FromPath(source))))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.UIDir == "" || bundle.UIEntry == "" {
		t.Fatalf("sidecar UI was not associated with the DLL: %+v", bundle)
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

// TestManifestRejectsUnsafePaths covers manifest path normalization through
// readManifest, where the validation lives.
func TestManifestRejectsUnsafePaths(t *testing.T) {
	for _, manifest := range []string{
		`{"dll":"../escape.dll"}`,
		`{"dll":"C:\\escape.dll"}`,
		`{"dll":"C:/escape.dll"}`,
		`{"ui":"\\\\server\\\\share"}`,
		`{"ui":"/etc/escape"}`,
		`{"uiEntry":"a/../../b"}`,
	} {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "plugin.json"), manifest)
		if _, err := readManifest(root); err == nil {
			t.Fatalf("expected %s to be rejected", manifest)
		}
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "plugin.json"),
		`{"dll":"bin/mod.dll","ui":"ui","uiEntry":"ui/index.html"}`)
	manifest, err := readManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.DLL != "bin/mod.dll" || manifest.UI != "ui" || manifest.UIEntry != "ui/index.html" {
		t.Fatalf("manifest paths were not normalized: %+v", manifest)
	}
}

func TestCanonical(t *testing.T) {
	tests := map[string]string{
		"Example.Author.Mod": "example.author.mod",
		"my_mod-2":           "my_mod-2",
		".":                  "",
		"..":                 "",
		"...":                "",
		"bad id":             "",
		"":                   "",
	}
	for input, want := range tests {
		if got := Canonical(input); got != want {
			t.Fatalf("Canonical(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestFromPath(t *testing.T) {
	tests := map[string]string{
		`C:\mods\My-Mod.dll`:     "my-mod.dll",
		`C:\mods\My-Mod.14.dll`:  "my-mod.dll",
		`C:\mods\My-Mod.1.4.dll`: "my-mod.1.4.dll",
	}
	for input, want := range tests {
		if got := FromPath(input); got != want {
			t.Fatalf("FromPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDirectoryNameIsStableAndSafe(t *testing.T) {
	if got := DirectoryName("example.author.mod"); got != "example.author.mod" {
		t.Fatalf("unexpected safe id: %q", got)
	}
	if got := DirectoryName("My Mod"); got == "" || got == "my mod" {
		t.Fatalf("unsafe directory name: %q", got)
	}
	if DirectoryName("My Mod") != DirectoryName("My Mod") {
		t.Fatal("directory name is not deterministic")
	}
}

func TestDirectoryNameRejectsWindowsSpecialNames(t *testing.T) {
	for _, id := range []string{".", "..", "...", ".hidden.", "CON", "con.txt", "NUL", "COM1", "LPT9"} {
		name := DirectoryName(id)
		if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") {
			t.Fatalf("DirectoryName(%q) = %q", id, name)
		}
		if name == strings.ToLower(id) {
			t.Fatalf("DirectoryName(%q) returned an unsafe name unchanged", id)
		}
	}
}

func TestReadVersionInfoRejectsNonPE(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-pe.dll")
	writeFile(t, path, "not a PE file")
	if _, err := readVersionInfo(path); err == nil {
		t.Fatal("expected a file without VERSIONINFO to fail")
	}
}

func TestImportTallySummaries(t *testing.T) {
	tests := []struct {
		name    string
		actions []Action
		items   int
		want    string
		confirm bool
	}{
		{"nothing selected", nil, 0, "No plugin files were selected.", false},
		{"added and updated", []Action{ActionAdded, ActionUpdated, ActionAdded}, 3,
			"Imported 3 plugin(s): 2 added, 1 updated.", false},
		{"unchanged only", []Action{ActionUnchanged}, 1, "No plugin files were changed.", false},
		{"conflict needs confirmation", []Action{ActionConflict, ActionAdded}, 2,
			"One or more plugins already exist. Confirm replacement to continue.", true},
		{"failure after success", []Action{ActionAdded, ActionFailed}, 2,
			"Imported 1 plugin(s): 1 added, 0 updated. 1 plugin(s) could not be imported.", false},
		{"failure only", []Action{ActionFailed}, 1,
			"No plugin files were changed. 1 plugin(s) could not be imported.", false},
		{"conflict and failure", []Action{ActionConflict, ActionFailed}, 2,
			"One or more plugins already exist. Confirm replacement to continue. 1 plugin(s) could not be imported.", true},
	}
	for _, test := range tests {
		var tally importTally
		for _, action := range test.actions {
			tally.count(action)
		}
		got, confirm := tally.summarize(test.items)
		if got != test.want || confirm != test.confirm {
			t.Errorf("%s: summarize = (%q, %v), want (%q, %v)", test.name, got, confirm, test.want, test.confirm)
		}
	}
}

func TestPluginRecordHelpers(t *testing.T) {
	bundle := &pluginRecord{DLLPath: `C:\store\p\bin\p.dll`, BundleRoot: `C:\store\p`, UIDir: `C:\store\p\ui`, UIEntry: "index.html"}
	if got := bundle.folder(); got != `C:\store\p` {
		t.Fatalf("bundle folder = %q", got)
	}
	if !bundle.hasUI() {
		t.Fatal("bundle with UI directory and entry should report a UI")
	}

	legacy := &pluginRecord{DLLPath: `C:\mods\legacy.dll`, UIDir: `C:\mods\ui`}
	if got := legacy.folder(); got != `C:\mods` {
		t.Fatalf("legacy folder = %q", got)
	}
	if legacy.hasUI() || (&pluginRecord{UIEntry: "index.html"}).hasUI() {
		t.Fatal("a UI needs both a directory and an entry")
	}
}
