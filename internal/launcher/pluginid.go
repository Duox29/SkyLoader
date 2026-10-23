package launcher

import (
	"crypto/sha1"
	"encoding/hex"
	"path"
	"strings"
)

const maxLength = 128

// Canonical returns the lower-case identity when value is a valid SkyPluginId,
// and an empty string otherwise.
func Canonical(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength || strings.Trim(value, ".") == "" {
		return ""
	}
	out := make([]byte, len(value))
	for i := 0; i < len(value); i++ {
		character := value[i]
		switch {
		case character >= 'a' && character <= 'z':
			out[i] = character
		case character >= 'A' && character <= 'Z':
			out[i] = character + ('a' - 'A')
		case character >= '0' && character <= '9':
			out[i] = character
		case character == '.' || character == '_' || character == '-':
			out[i] = character
		default:
			return ""
		}
	}
	return string(out)
}

// FromPath derives the legacy filename-based identity used for DLLs without a
// SkyPluginId. A trailing numeric release suffix (my-mod.14.dll) is ignored.
func FromPath(filePath string) string {
	filename := strings.ToLower(windowsBaseName(filePath))
	extension := path.Ext(filename)
	stem := strings.TrimSuffix(filename, extension)

	// Only strip a suffix such as ".14" from a simple filename. Dotted stems
	// like "my-mod.1.4" are version numbers and must stay intact.
	if dot := strings.LastIndexByte(stem, '.'); dot >= 0 && dot+1 < len(stem) &&
		strings.IndexByte(stem, '.') == dot {
		numeric := true
		for i := dot + 1; i < len(stem); i++ {
			if stem[i] < '0' || stem[i] > '9' {
				numeric = false
				break
			}
		}
		if numeric {
			stem = stem[:dot]
		}
	}
	return stem + extension
}

// DirectoryName converts an identity into a safe directory name. Valid IDs are
// already safe; legacy identities are sanitized and hashed when necessary.
func DirectoryName(id string) string {
	canonical := Canonical(id)
	if canonical != "" && len(canonical) <= 80 && safeDirectoryName(canonical) {
		return canonical
	}

	sanitized := make([]byte, 0, len(id))
	for i := 0; i < len(id) && len(sanitized) < 64; i++ {
		character := id[i]
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
			sanitized = append(sanitized, character)
		case character >= 'A' && character <= 'Z':
			sanitized = append(sanitized, character+('a'-'A'))
		default:
			sanitized = append(sanitized, '_')
		}
	}
	if len(sanitized) == 0 {
		sanitized = []byte("plugin")
	}
	sum := sha1.Sum([]byte(id))
	return string(sanitized) + "-" + hex.EncodeToString(sum[:4])
}

// safeDirectoryName rejects names that Win32 would reinterpret. Dots alone or a
// trailing dot collapse to a different path (or the parent directory), and
// device names such as CON or NUL are reserved by the filesystem.
func safeDirectoryName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") {
		return false
	}
	stem := name
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

// DisplayName returns a readable fallback for a plugin without a PluginName.
func DisplayName(filePath string) string {
	base := windowsBaseName(filePath)
	return strings.TrimSuffix(base, path.Ext(base))
}
