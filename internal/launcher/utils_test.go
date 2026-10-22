package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsHTTPURL(t *testing.T) {
	for _, value := range []string{
		"http://example.com", "https://example.com/a?b=c", "HTTPS://EXAMPLE.COM", "Http://x",
	} {
		if !isHTTPURL(value) {
			t.Errorf("isHTTPURL(%q) = false, want true", value)
		}
	}
	// Callers trim whitespace first, so a leading space is not accepted here.
	for _, value := range []string{
		"", "ftp://example.com", "javascript:alert(1)", "file:///C:/Windows", "httpx://x",
		"example.com", " https://example.com", "steam://run/1",
	} {
		if isHTTPURL(value) {
			t.Errorf("isHTTPURL(%q) = true, want false", value)
		}
	}
}

func TestIsDigits(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, "0": true, "14": true, "1a": false, "-1": false, "1.4": false, "\u0663": false,
	} {
		if got := isDigits(value); got != want {
			t.Errorf("isDigits(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestEncodeJSON(t *testing.T) {
	data, err := encodeJSON(Manifest{ID: "a.b", DLL: "a.dll"})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"id\": \"a.b\",\n  \"dll\": \"a.dll\"\n}\n"
	if string(data) != want {
		t.Fatalf("encodeJSON = %q, want %q", data, want)
	}
	if _, err := encodeJSON(make(chan int)); err == nil {
		t.Fatal("expected an unencodable value to fail")
	}
}

func TestCanonicalCharacterRules(t *testing.T) {
	tests := map[string]string{
		"Mixed.Case_1-x":         "mixed.case_1-x",
		"  padded.id  ":          "padded.id",
		"a/b":                    "",
		`a\b`:                    "",
		"na\u00efve":             "",
		strings.Repeat("A", 128): strings.Repeat("a", 128),
		strings.Repeat("a", 129): "",
	}
	for input, want := range tests {
		if got := Canonical(input); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDirectoryNameSanitizesUnsafeIDs(t *testing.T) {
	tests := []struct {
		id         string
		prefix     string
		wantLength int // sanitized stem + "-" + 8 hash characters
	}{
		{"My Mod", "my_mod-", len("my_mod") + 9},
		{"\u00e9", "__-", 2 + 9}, // two UTF-8 bytes, each replaced
		{"", "plugin-", len("plugin") + 9},
		{"CON", "con-", len("con") + 9},
		{strings.Repeat("A!", 100), strings.Repeat("a_", 32) + "-", 64 + 9},
	}
	for _, test := range tests {
		got := DirectoryName(test.id)
		if !strings.HasPrefix(got, test.prefix) || len(got) != test.wantLength {
			t.Errorf("DirectoryName(%q) = %q, want prefix %q and length %d", test.id, got, test.prefix, test.wantLength)
		}
	}
}

func TestFromPathNumericSuffix(t *testing.T) {
	tests := map[string]string{
		`C:\mods\x.14.dll`:    "x.dll",
		`C:\mods\x.dll`:       "x.dll",
		`C:\mods\a.b.14.dll`:  "a.b.14.dll",
		`C:\mods\x..dll`:      "x..dll",
		`C:\mods\x.1a.dll`:    "x.1a.dll",
		`C:\mods\.14.dll`:     ".dll",
		"/posix/dir/Y.3.DLL":  "y.dll",
		`C:\mods\NoExt.7`:     "noext.7", // ".7" is the extension, not a release suffix
		`C:\mods\v1.2.3.dll`:  "v1.2.3.dll",
		`C:\mods\Plugin.9.so`: "plugin.so",
	}
	for input, want := range tests {
		if got := FromPath(input); got != want {
			t.Errorf("FromPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHasDLL(t *testing.T) {
	root := t.TempDir()
	if hasDLL(root) {
		t.Fatal("empty directory reported a DLL")
	}
	if err := os.MkdirAll(filepath.Join(root, "folder.dll"), 0o755); err != nil {
		t.Fatal(err)
	}
	if hasDLL(root) {
		t.Fatal("a directory named like a DLL was counted")
	}
	nested := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "Plugin.DLL"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasDLL(root) {
		t.Fatal("nested, upper-case .DLL was not found")
	}
	if hasDLL(filepath.Join(root, "missing")) {
		t.Fatal("missing directory reported a DLL")
	}
}

func TestResolveBundleRoot(t *testing.T) {
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	withManifest := t.TempDir()
	write(filepath.Join(withManifest, ManifestName))
	if got, err := resolveBundleRoot(withManifest); err != nil || got != withManifest {
		t.Fatalf("manifest at root: got %q, %v", got, err)
	}

	wrapped := t.TempDir()
	write(filepath.Join(wrapped, "wrap", "only.dll"))
	if got, err := resolveBundleRoot(wrapped); err != nil || got != filepath.Join(wrapped, "wrap") {
		t.Fatalf("single wrapper folder: got %q, %v", got, err)
	}

	twoFolders := t.TempDir()
	write(filepath.Join(twoFolders, "one", "a.dll"))
	write(filepath.Join(twoFolders, "two", "b.txt"))
	if got, err := resolveBundleRoot(twoFolders); err != nil || got != twoFolders {
		t.Fatalf("two folders with a nested DLL: got %q, %v", got, err)
	}

	if _, err := resolveBundleRoot(t.TempDir()); err == nil {
		t.Fatal("empty directory should not resolve to a bundle root")
	}
}
