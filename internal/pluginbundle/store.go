package pluginbundle

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HerokeyVN/SkyLoader/internal/pluginid"
	"github.com/HerokeyVN/SkyLoader/internal/versioncmp"
	"github.com/HerokeyVN/SkyLoader/internal/winver"
)

// Action describes the outcome of one import.
type Action string

const (
	ActionAdded     Action = "added"
	ActionUpdated   Action = "updated"
	ActionUnchanged Action = "unchanged"
	ActionConflict  Action = "conflict"
	ActionFailed    Action = "failed"
)

// Result is the user-facing outcome of importing one path.
type Result struct {
	Action  Action
	ID      string
	Name    string
	Version string
	Path    string
	UIPath  string
	Message string
}

const (
	maxBundleBytes = 300 << 20
	maxBundleFiles = 10000
)

// Store is the managed plugin directory. Each plugin lives in its own
// directory so that a DLL and its UI bundle are installed and replaced as one
// unit.
type Store struct {
	Root string
}

// New creates the managed store if necessary.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("plugin store directory is empty")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	return &Store{Root: root}, nil
}

// Scan loads every valid bundle in the store.
func (s *Store) Scan() []Bundle {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return nil
	}
	var bundles []Bundle
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".stage-") || strings.HasPrefix(name, ".old-") {
			continue
		}
		bundle, err := Load(filepath.Join(s.Root, name))
		if err != nil {
			continue
		}
		bundles = append(bundles, bundle)
	}
	return bundles
}

// Import installs a DLL, a zip bundle, or an unpacked bundle directory.
func (s *Store) Import(input string, force bool) Result {
	input, err := filepath.Abs(input)
	if err != nil {
		return Result{Action: ActionFailed, Message: err.Error()}
	}
	info, err := os.Stat(input)
	if err != nil {
		return Result{Action: ActionFailed, Message: "Plugin path was not found."}
	}

	stage, err := os.MkdirTemp(s.Root, ".stage-")
	if err != nil {
		return Result{Action: ActionFailed, Message: err.Error()}
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()

	switch {
	case info.IsDir():
		resolved, err := resolveBundleRoot(input)
		if err != nil {
			return Result{Action: ActionFailed, Message: err.Error()}
		}
		if err := copyTree(resolved, stage); err != nil {
			return Result{Action: ActionFailed, Message: err.Error()}
		}
	case strings.EqualFold(filepath.Ext(input), ".zip"):
		if err := extractZip(input, stage); err != nil {
			return Result{Action: ActionFailed, Message: err.Error()}
		}
		resolved, err := resolveBundleRoot(stage)
		if err != nil {
			return Result{Action: ActionFailed, Message: err.Error()}
		}
		if resolved != stage {
			if err := moveContents(resolved, stage); err != nil {
				return Result{Action: ActionFailed, Message: err.Error()}
			}
		}
	case strings.EqualFold(filepath.Ext(input), ".dll"):
		if err := stageDLL(input, stage); err != nil {
			return Result{Action: ActionFailed, Message: err.Error()}
		}
	default:
		return Result{Action: ActionFailed, Message: "Drop a .dll, a .zip bundle, or a bundle folder."}
	}

	bundle, err := Load(stage)
	if err != nil {
		return Result{Action: ActionFailed, Message: err.Error()}
	}

	result := s.install(stage, bundle, force)
	if result.Action == ActionAdded || result.Action == ActionUpdated {
		keepStage = true // install moved the stage into place
	}
	return result
}

func (s *Store) install(stage string, bundle Bundle, force bool) Result {
	destination := filepath.Join(s.Root, pluginid.DirectoryName(bundle.ID))
	if _, err := os.Stat(destination); err == nil {
		existing, loadErr := Load(destination)
		if loadErr == nil && !force {
			if existing.ID != bundle.ID {
				return Result{
					Action:  ActionConflict,
					ID:      bundle.ID,
					Name:    bundle.Name,
					Version: bundle.Version,
					Message: fmt.Sprintf("The destination already contains plugin %s.", existing.ID),
				}
			}
			if existing.Version != "" && bundle.Version != "" {
				comparison := versioncmp.Compare(bundle.Version, existing.Version)
				if comparison < 0 {
					return Result{
						Action:  ActionUnchanged,
						ID:      bundle.ID,
						Name:    bundle.Name,
						Version: bundle.Version,
						Message: fmt.Sprintf("Newer %s version %s is already installed.", existing.Name, existing.Version),
					}
				}
			} else if (existing.Version == "") != (bundle.Version == "") {
				return Result{
					Action:  ActionConflict,
					ID:      bundle.ID,
					Name:    bundle.Name,
					Version: bundle.Version,
					Message: "Installed and incoming builds disagree about version metadata. Replace it?",
				}
			} else if !strings.EqualFold(filepath.Base(existing.DLLPath), filepath.Base(bundle.DLLPath)) {
				return Result{
					Action:  ActionConflict,
					ID:      bundle.ID,
					Name:    bundle.Name,
					Version: bundle.Version,
					Message: "A plugin with the same name is already installed. Replace it?",
				}
			}
		}

		old := filepath.Join(s.Root, ".old-"+randomSuffix())
		if err := os.Rename(destination, old); err != nil {
			return Result{
				Action:  ActionFailed,
				ID:      bundle.ID,
				Message: "The installed plugin is in use. Close Sky and try again.",
			}
		}
		if err := os.Rename(stage, destination); err != nil {
			_ = os.Rename(old, destination)
			return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}
		}
		_ = os.RemoveAll(old)
		return resultFor(ActionUpdated, bundle, destination)
	}

	if err := os.Rename(stage, destination); err != nil {
		return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}
	}
	return resultFor(ActionAdded, bundle, destination)
}

func resultFor(action Action, bundle Bundle, destination string) Result {
	relativeDLL, err := filepath.Rel(bundle.Root, bundle.DLLPath)
	if err != nil {
		relativeDLL = filepath.Base(bundle.DLLPath)
	}
	result := Result{
		Action:  action,
		ID:      bundle.ID,
		Name:    bundle.Name,
		Version: bundle.Version,
		Path:    filepath.Join(destination, relativeDLL),
		Message: "Plugin " + string(action) + ".",
	}
	if bundle.HasUI() {
		relative, _ := filepath.Rel(bundle.Root, bundle.UIDir)
		result.UIPath = filepath.Join(destination, relative)
	}
	return result
}

// stageDLL copies a single DLL plus an optional sidecar UI directory.
func stageDLL(source, stage string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.Size() > maxBundleBytes {
		return errors.New("DLL is too large")
	}
	if err := copyFile(source, filepath.Join(stage, filepath.Base(source))); err != nil {
		return err
	}

	directory := filepath.Dir(source)
	stem := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	for _, candidate := range []string{
		filepath.Join(directory, stem+".ui"),
		filepath.Join(directory, "ui"),
	} {
		if isRegularFile(filepath.Join(candidate, "index.html")) {
			if err := copyTree(candidate, filepath.Join(stage, "ui")); err != nil {
				return err
			}
			break
		}
	}

	manifest := Manifest{
		ID:   "",
		Name: "",
		DLL:  filepath.Base(source),
	}
	if metadata, err := winver.Read(source); err == nil {
		manifest.ID = metadata.ID
		manifest.Name = metadata.Name
		manifest.Version = metadata.Version
		manifest.Author = metadata.Author
		manifest.Description = metadata.Description
	}
	if manifest.ID == "" {
		manifest.ID = pluginid.FromPath(source)
	}
	// A filename-derived identity may contain characters that are not valid in
	// SkyPluginId. Leave the manifest field empty in that case so Load falls
	// back to the same legacy filename identity.
	if pluginid.Canonical(manifest.ID) == "" {
		manifest.ID = ""
	}
	if isRegularFile(filepath.Join(stage, "ui", "index.html")) {
		manifest.UI = "ui"
	}
	return writeManifest(stage, manifest)
}

func writeManifest(root string, manifest Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(root, ManifestName), data, 0o644)
}

// LoadForDLL loads the bundle that owns dllPath when a plugin.json sits next
// to the DLL.
func LoadForDLL(dllPath string) (Bundle, bool) {
	directory := filepath.Dir(dllPath)
	if !isRegularFile(filepath.Join(directory, ManifestName)) {
		return Bundle{}, false
	}
	bundle, err := Load(directory)
	if err != nil || !strings.EqualFold(bundle.DLLPath, dllPath) {
		return Bundle{}, false
	}
	return bundle, true
}

// FindUI resolves the UI directory for an arbitrary DLL, including legacy
// plugin paths that are not managed bundles.
func FindUI(dllPath string) (uiDir, entry string) {
	if bundle, ok := LoadForDLL(dllPath); ok {
		return bundle.UIDir, bundle.UIEntry
	}
	directory := filepath.Dir(dllPath)
	stem := strings.TrimSuffix(filepath.Base(dllPath), filepath.Ext(dllPath))
	candidates := []string{
		filepath.Join(directory, stem+".ui"),
		filepath.Join(directory, "ui"),
	}
	for _, candidate := range candidates {
		if isRegularFile(filepath.Join(candidate, "index.html")) {
			return candidate, "index.html"
		}
	}
	return "", ""
}

func resolveBundleRoot(directory string) (string, error) {
	if isRegularFile(filepath.Join(directory, ManifestName)) {
		return directory, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, filepath.Join(directory, entry.Name()))
		}
	}
	if len(directories) == 1 {
		child := directories[0]
		if isRegularFile(filepath.Join(child, ManifestName)) {
			return child, nil
		}
		if hasDLL(child) {
			return child, nil
		}
	}
	if hasDLL(directory) {
		return directory, nil
	}
	return "", errors.New("bundle has no plugin.json or DLL")
}

func hasDLL(directory string) bool {
	found := false
	_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || found {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			found = true
		}
		return nil
	})
	return found
}

func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	var total int64
	files := 0
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle contains a symbolic link: %s", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		files++
		if total > maxBundleBytes {
			return errors.New("bundle is too large")
		}
		if files > maxBundleFiles {
			return errors.New("bundle contains too many files")
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, maxBundleBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func moveContents(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(source, entry.Name())
		to := filepath.Join(destination, entry.Name())
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	return nil
}

func randomSuffix() string {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "tmp"
	}
	return hex.EncodeToString(bytes)
}
