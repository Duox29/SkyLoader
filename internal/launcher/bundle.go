package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const ManifestName = "plugin.json"

// Manifest describes a plugin bundle. All paths are relative to the bundle
// root. The DLL's VERSIONINFO remains the source of truth for a SkyPluginId;
// the manifest may repeat it for convenience.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	DLL         string `json:"dll,omitempty"`
	UI          string `json:"ui,omitempty"`
	UIEntry     string `json:"uiEntry,omitempty"`
}

// Bundle is a loaded and validated plugin bundle.
type Bundle struct {
	Root        string
	Manifest    Manifest
	ID          string
	DLLPath     string
	Version     string
	Name        string
	Author      string
	Description string
	UIDir       string
	UIEntry     string
}

// HasUI reports whether the bundle has a usable index page.
func (b Bundle) HasUI() bool {
	return b.UIDir != "" && b.UIEntry != ""
}

// Load validates the bundle rooted at root and resolves its DLL and UI paths.
func Load(root string) (Bundle, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Bundle{}, err
	}
	manifest, err := readManifest(root)
	if err != nil {
		return Bundle{}, err
	}

	dllPath, err := findDLL(root, manifest)
	if err != nil {
		return Bundle{}, err
	}
	info, _ := readVersionInfo(dllPath)

	id := Canonical(manifest.ID)
	if manifest.ID != "" && id == "" {
		return Bundle{}, fmt.Errorf("invalid bundle id %q", manifest.ID)
	}
	if id == "" {
		id = info.ID
	}
	if id == "" {
		id = FromPath(dllPath)
	}
	if manifest.ID != "" && info.ID != "" && id != info.ID {
		return Bundle{}, fmt.Errorf("bundle id %q does not match DLL SkyPluginId %q", id, info.ID)
	}

	bundle := Bundle{
		Root:        root,
		Manifest:    manifest,
		ID:          id,
		DLLPath:     dllPath,
		Version:     firstNonEmpty(manifest.Version, info.Version),
		Name:        firstNonEmpty(manifest.Name, info.Name, DisplayName(dllPath)),
		Author:      firstNonEmpty(manifest.Author, info.Author),
		Description: firstNonEmpty(manifest.Description, info.Description),
	}

	uiDir, uiEntry, err := resolveUI(root, manifest)
	if err != nil {
		return Bundle{}, err
	}
	bundle.UIDir = uiDir
	bundle.UIEntry = uiEntry
	return bundle, nil
}

func readManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", ManifestName, err)
	}
	if manifest.UI != "" {
		clean, err := safeRelative(manifest.UI)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest ui: %w", err)
		}
		manifest.UI = clean
	}
	if manifest.UIEntry != "" {
		clean, err := safeRelative(manifest.UIEntry)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest uiEntry: %w", err)
		}
		manifest.UIEntry = clean
	}
	if manifest.DLL != "" {
		clean, err := safeRelative(manifest.DLL)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest dll: %w", err)
		}
		manifest.DLL = clean
	}
	return manifest, nil
}

func findDLL(root string, manifest Manifest) (string, error) {
	if manifest.DLL != "" {
		path := filepath.Join(root, filepath.FromSlash(manifest.DLL))
		if !isRegularFile(path) {
			return "", fmt.Errorf("bundle DLL %q was not found", manifest.DLL)
		}
		return path, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var dlls []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			dlls = append(dlls, entry.Name())
		}
	}
	switch len(dlls) {
	case 0:
		return "", errors.New("bundle has no DLL")
	case 1:
		return filepath.Join(root, dlls[0]), nil
	default:
		base := strings.ToLower(filepath.Base(root))
		for _, name := range dlls {
			if strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) == base {
				return filepath.Join(root, name), nil
			}
		}
		return "", errors.New("bundle has multiple DLLs; set \"dll\" in plugin.json")
	}
}

func resolveUI(root string, manifest Manifest) (string, string, error) {
	uiDir := manifest.UI
	if uiDir == "" {
		candidate := filepath.Join(root, "ui")
		if isRegularFile(filepath.Join(candidate, "index.html")) {
			uiDir = "ui"
		}
	}
	if uiDir == "" {
		return "", "", nil
	}

	absolute := filepath.Join(root, filepath.FromSlash(uiDir))
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("bundle UI directory %q was not found", uiDir)
	}
	entry := manifest.UIEntry
	if entry == "" {
		entry = filepath.ToSlash(filepath.Join(uiDir, "index.html"))
	} else if !strings.HasPrefix(filepath.ToSlash(entry), filepath.ToSlash(uiDir)+"/") {
		// uiEntry may be relative to the UI directory or to the bundle root.
		entry = filepath.ToSlash(filepath.Join(uiDir, entry))
	}
	entryPath := filepath.Join(root, filepath.FromSlash(entry))
	if !isRegularFile(entryPath) {
		return "", "", fmt.Errorf("bundle UI entry %q was not found", entry)
	}
	relativeEntry, err := filepath.Rel(absolute, entryPath)
	if err != nil {
		return "", "", err
	}
	return absolute, filepath.ToSlash(relativeEntry), nil
}

func safeRelative(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "/") || filepath.IsAbs(value) ||
		filepath.VolumeName(value) != "" || (len(value) >= 2 && value[1] == ':') {
		return "", errors.New("must be relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("must stay inside the bundle")
	}
	return strings.TrimPrefix(cleaned, "./"), nil
}
