package launcher

import (
	"strings"
	"testing"
)

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
