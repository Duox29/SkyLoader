//go:build windows

package launcher

import (
	"archive/zip"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HerokeyVN/SkyLoader/internal/uiserver"
)

const (
	ManifestName   = "plugin.json"
	maxLength      = 128
	maxBundleBytes = 300 << 20
	maxBundleFiles = 10000
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
	Message string
}

// Manifest describes a plugin bundle. All paths are relative to the bundle
// root. The DLL's VERSIONINFO remains the source of truth for a SkyPluginId;
// the manifest may repeat it for convenience. readVersionInfo also docks its
// VERSIONINFO-derived metadata in this struct, without path fields.
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

// Canonical returns the lower-case identity when value is a valid SkyPluginId,
// and an empty string otherwise.
func Canonical(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength || strings.Trim(value, ".") == "" {
		return ""
	}
	out := []byte(value)
	for i, character := range out {
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
		case character >= 'A' && character <= 'Z':
			out[i] = character + ('a' - 'A')
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
	extension := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, extension)
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
//
// Names that Win32 would reinterpret fall back to the hashed branch: a
// trailing dot collapses to a different path, and device names such as CON or
// NUL are reserved by the filesystem. Canonical identities are lower-case.
func DirectoryName(id string) string {
	if canonical := Canonical(id); canonical != "" && len(canonical) <= 80 &&
		!strings.HasSuffix(canonical, ".") {
		stem := canonical
		if dot := strings.IndexByte(stem, '.'); dot >= 0 {
			stem = stem[:dot]
		}
		switch stem {
		case "con", "prn", "aux", "nul",
			"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
			"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		default:
			return canonical
		}
	}

	sanitized := make([]byte, 0, 64)
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

// DisplayName returns a readable fallback for a plugin without a PluginName.
func DisplayName(filePath string) string {
	base := windowsBaseName(filePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// readManifest reads and normalizes the plugin.json document of the bundle
// rooted at root. A missing manifest yields an empty value.
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
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"ui", &manifest.UI},
		{"uiEntry", &manifest.UIEntry},
		{"dll", &manifest.DLL},
	} {
		if *field.value == "" {
			continue
		}
		// Declared paths must stay inside the bundle: trim Windows-style
		// separators, then reject absolute and escaping paths.
		relative := strings.TrimSpace(strings.ReplaceAll(*field.value, "\\", "/"))
		clean := filepath.Clean(filepath.FromSlash(relative))
		if strings.HasPrefix(relative, "/") || (len(relative) >= 2 && relative[1] == ':') ||
			clean == "." || !filepath.IsLocal(clean) {
			return Manifest{}, fmt.Errorf("manifest %s: must be a relative path inside the bundle", field.name)
		}
		*field.value = filepath.ToSlash(clean)
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
		if isRegularFile(filepath.Join(root, "ui", "index.html")) {
			uiDir = "ui"
		} else {
			return "", "", nil
		}
	}
	absolute := filepath.Join(root, filepath.FromSlash(uiDir))
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
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

// loadBundle validates the bundle rooted at root and resolves its DLL and UI
// paths. The returned record is not managed until the caller says so.
func loadBundle(root string) (*pluginRecord, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	manifest, err := readManifest(root)
	if err != nil {
		return nil, err
	}
	dllPath, err := findDLL(root, manifest)
	if err != nil {
		return nil, err
	}
	info, _ := readVersionInfo(dllPath)

	id := Canonical(manifest.ID)
	if manifest.ID != "" && id == "" {
		return nil, fmt.Errorf("invalid bundle id %q", manifest.ID)
	}
	if id == "" {
		id = info.ID
	}
	if id == "" {
		id = FromPath(dllPath)
	}
	if manifest.ID != "" && info.ID != "" && id != info.ID {
		return nil, fmt.Errorf("bundle id %q does not match DLL SkyPluginId %q", id, info.ID)
	}

	uiDir, uiEntry, err := resolveUI(root, manifest)
	if err != nil {
		return nil, err
	}
	return &pluginRecord{
		ID:          id,
		Name:        firstNonEmpty(manifest.Name, info.Name, DisplayName(dllPath)),
		Version:     firstNonEmpty(manifest.Version, info.Version),
		Author:      firstNonEmpty(manifest.Author, info.Author),
		Description: firstNonEmpty(manifest.Description, info.Description),
		DLLPath:     dllPath,
		BundleRoot:  root,
		UIDir:       uiDir,
		UIEntry:     uiEntry,
		Enabled:     true,
	}, nil
}

// bundleForDLL loads the bundle that owns dllPath when a plugin.json sits next
// to the DLL.
func bundleForDLL(dllPath string) (*pluginRecord, bool) {
	directory := filepath.Dir(dllPath)
	if !isRegularFile(filepath.Join(directory, ManifestName)) {
		return nil, false
	}
	bundle, err := loadBundle(directory)
	if err != nil || !strings.EqualFold(bundle.DLLPath, dllPath) {
		return nil, false
	}
	return bundle, true
}

func recordForDLL(path string) *pluginRecord {
	record := &pluginRecord{ID: FromPath(path), DLLPath: path, Enabled: true}
	if bundle, ok := bundleForDLL(path); ok {
		return bundle
	}
	if info, err := readVersionInfo(path); err == nil {
		record.ID = info.ID
		record.Name = info.Name
		record.Version = info.Version
		record.Author = info.Author
		record.Description = info.Description
	}
	// Legacy plugins may keep their UI next to the DLL instead of inside a
	// plugin.json bundle.
	directory := filepath.Dir(path)
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for _, candidate := range []string{
		filepath.Join(directory, stem+".ui"),
		filepath.Join(directory, "ui"),
	} {
		if isRegularFile(filepath.Join(candidate, "index.html")) {
			record.UIDir, record.UIEntry = candidate, "index.html"
			break
		}
	}
	return record
}

func scanBundles(root string) []*pluginRecord {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var bundles []*pluginRecord
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".stage-") || strings.HasPrefix(name, ".old-") {
			continue
		}
		if bundle, err := loadBundle(filepath.Join(root, name)); err == nil {
			bundles = append(bundles, bundle)
		}
	}
	return bundles
}

func pluginResult(action Action, bundle *pluginRecord, message string) Result {
	return Result{Action: action, ID: bundle.ID, Name: bundle.Name, Version: bundle.Version, Message: message}
}

func importError(err error) Result {
	return Result{Action: ActionFailed, Message: err.Error()}
}

// importPlugin installs a DLL, a zip bundle, or an unpacked bundle directory
// into the managed store. Missing destinations are added as-is; existing ones
// go through the version/dll-name conflict policy and are replaced by
// renaming the staged copy into place after the old directory is parked in a
// sibling .old- directory (locked files on Windows cannot be deleted in
// place, but an empty directory can be renamed).
func importPlugin(root, input string, force bool) Result {
	input, err := filepath.Abs(input)
	if err != nil {
		return importError(err)
	}
	info, err := os.Stat(input)
	if err != nil {
		return Result{Action: ActionFailed, Message: "Plugin path was not found."}
	}

	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return importError(err)
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
			return importError(err)
		}
		if err := copyTree(resolved, stage); err != nil {
			return importError(err)
		}
	case strings.EqualFold(filepath.Ext(input), ".zip"):
		if err := extractZip(input, stage); err != nil {
			return importError(err)
		}
		resolved, err := resolveBundleRoot(stage)
		if err != nil {
			return importError(err)
		}
		if resolved != stage {
			if err := moveContents(resolved, stage); err != nil {
				return importError(err)
			}
		}
	case strings.EqualFold(filepath.Ext(input), ".dll"):
		if err := stageDLL(input, stage); err != nil {
			return importError(err)
		}
	default:
		return Result{Action: ActionFailed, Message: "Drop a .dll, a .zip bundle, or a bundle folder."}
	}

	bundle, err := loadBundle(stage)
	if err != nil {
		return importError(err)
	}

	destination := filepath.Join(root, DirectoryName(bundle.ID))
	if _, err := os.Stat(destination); err != nil {
		if err := os.Rename(stage, destination); err != nil {
			return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}
		}
		keepStage = true // the stage moved into place
		return pluginResult(ActionAdded, bundle, "Plugin added.")
	}

	existing, loadErr := loadBundle(destination)
	if loadErr == nil && !force {
		if existing.ID != bundle.ID {
			return pluginResult(ActionConflict, bundle,
				fmt.Sprintf("The destination already contains plugin %s.", existing.ID))
		}
		if existing.Version != "" && bundle.Version != "" {
			if compareVersions(bundle.Version, existing.Version) < 0 {
				return pluginResult(ActionUnchanged, bundle,
					fmt.Sprintf("Newer %s version %s is already installed.", existing.Name, existing.Version))
			}
		} else if (existing.Version == "") != (bundle.Version == "") {
			return pluginResult(ActionConflict, bundle,
				"Installed and incoming builds disagree about version metadata. Replace it?")
		} else if !strings.EqualFold(filepath.Base(existing.DLLPath), filepath.Base(bundle.DLLPath)) {
			return pluginResult(ActionConflict, bundle,
				"A plugin with the same name is already installed. Replace it?")
		}
	}

	suffix := "tmp"
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err == nil {
		suffix = hex.EncodeToString(bytes)
	}
	old := filepath.Join(root, ".old-"+suffix)
	if err := os.Rename(destination, old); err != nil {
		return Result{
			Action: ActionFailed, ID: bundle.ID,
			Message: "The installed plugin is in use. Close Sky and try again.",
		}
	}
	if err := os.Rename(stage, destination); err != nil {
		_ = os.Rename(old, destination)
		return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}
	}
	_ = os.RemoveAll(old)
	keepStage = true // the stage moved into place
	return pluginResult(ActionUpdated, bundle, "Plugin updated.")
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

	// readVersionInfo answers a Manifest carrying only the DLL's
	// VERSIONINFO metadata; the staged manifest still needs its own path.
	manifest := Manifest{DLL: filepath.Base(source)}
	if metadata, err := readVersionInfo(source); err == nil {
		manifest = metadata
		manifest.DLL = filepath.Base(source)
	}
	if manifest.ID == "" {
		manifest.ID = FromPath(source)
	}
	// A filename-derived identity may contain characters that are not valid in
	// SkyPluginId. Leave the manifest field empty in that case so loadBundle
	// falls back to the same legacy filename identity.
	if Canonical(manifest.ID) == "" {
		manifest.ID = ""
	}
	if isRegularFile(filepath.Join(stage, "ui", "index.html")) {
		manifest.UI = "ui"
	}

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(stage, ManifestName), data, 0o644)
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
		if isRegularFile(filepath.Join(child, ManifestName)) || hasDLL(child) {
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
		if err := os.Rename(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// extractZip extracts a bundle zip with traversal and zip-bomb protection.
func extractZip(source, destination string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return fmt.Errorf("open bundle zip: %w", err)
	}
	defer reader.Close()

	var total int64
	files := 0
	for _, entry := range reader.File {
		name := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(entry.Name, "\\", "/")))
		if name == "." || !filepath.IsLocal(name) {
			return fmt.Errorf("bundle contains an unsafe path: %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle contains a symbolic link: %q", entry.Name)
		}

		target := filepath.Join(destination, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		total += int64(entry.UncompressedSize64)
		files++
		if total > maxBundleBytes {
			return errors.New("bundle is too large")
		}
		if files > maxBundleFiles {
			return errors.New("bundle contains too many files")
		}
		if err := copyZipEntry(entry, target); err != nil {
			return err
		}
	}
	return nil
}

// copyZipEntry writes one zip member. Its parent directory is created before
// the member is opened, as folder imports do.
func copyZipEntry(entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// Import installs DLLs, zip bundles, or unpacked bundle directories into the
// managed store. A conflicting bundle is returned as an item with action
// "conflict"; retry the same call with force=true after confirmation.
func (l *Launcher) Import(paths []string, force bool) ImportReport {
	report := ImportReport{Paths: append([]string(nil), paths...), Force: force}
	var added, updated, conflicts, failed int

	for _, source := range paths {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		result := importPlugin(l.storeRoot, source, force)
		report.Items = append(report.Items, ImportItem{
			Source: source, Action: string(result.Action), ID: result.ID,
			Name: result.Name, Version: result.Version, Message: result.Message,
		})

		switch result.Action {
		case ActionAdded, ActionUpdated:
			if result.Action == ActionAdded {
				added++
			} else {
				updated++
			}
			l.afterInstall(result, source)
		case ActionConflict:
			conflicts++
		case ActionUnchanged:
		default:
			failed++
		}
	}

	switch {
	case conflicts > 0:
		report.NeedsConfirm = true
		report.Message = "One or more plugins already exist. Confirm replacement to continue."
	case added+updated > 0:
		report.Message = fmt.Sprintf("Imported %d plugin(s): %d added, %d updated.", added+updated, added, updated)
	case len(report.Items) > 0:
		report.Message = "No plugin files were changed."
	}
	if failed > 0 {
		if report.Message != "" {
			report.Message += " "
		}
		report.Message += fmt.Sprintf("%d plugin(s) could not be imported.", failed)
	}
	if len(report.Items) == 0 {
		report.Message = "No plugin files were selected."
	}
	if report.Message != "" {
		l.Notice("info", report.Message)
	}
	return report
}

func (l *Launcher) afterInstall(result Result, source string) {
	bundle, err := loadBundle(filepath.Join(l.storeRoot, DirectoryName(result.ID)))
	if err != nil {
		l.logger.Printf("reload imported bundle %s: %v", result.ID, err)
		return
	}
	bundle.Managed = true

	l.mu.Lock()
	key := strings.ToLower(bundle.ID)
	existing := l.plugins[key]
	if existing != nil {
		bundle.Enabled = existing.Enabled
	}
	l.plugins[key] = bundle
	if existing == nil {
		l.order = append(l.order, key)
	}
	sortPluginOrder(l.order, l.plugins)
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Plugin installed, but settings could not be saved: " + err.Error())
		return
	}

	if injected && bundle.Enabled {
		if err := l.client.Load(bundle.DLLPath); err != nil {
			l.setStatus("Plugin installed, but Sky did not load it: " + err.Error())
		} else {
			l.setStatus("Plugin installed and loaded into Sky.")
		}
		l.refreshLoaded()
	} else {
		l.setStatus("Plugin installed from " + filepath.Base(source) + ".")
	}
	l.notify()
}

// SetPluginEnabled toggles a plugin and, when Sky is already injected, applies
// the change to the running game.
func (l *Launcher) SetPluginEnabled(id string, enabled bool) error {
	l.mu.Lock()
	record := l.plugins[strings.ToLower(id)]
	if record == nil {
		l.mu.Unlock()
		return errors.New("plugin was not found")
	}
	record.Enabled = enabled
	dllPath := record.DLLPath
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Could not save the plugin setting: " + err.Error())
		return err
	}

	if injected {
		var pipeErr error
		if enabled {
			pipeErr = l.client.Load(dllPath)
		} else {
			pipeErr = l.client.Unload(dllPath)
		}
		if pipeErr != nil {
			l.setStatus("Setting saved, but Sky did not accept it: " + pipeErr.Error())
		} else if enabled {
			l.setStatus("Plugin loaded into Sky.")
		} else {
			l.setStatus("Plugin unloaded from Sky.")
		}
		l.refreshLoaded()
	} else {
		l.setStatus("Plugin setting saved.")
	}
	l.notify()
	return nil
}

// RemovePlugin unregisters a plugin. Managed bundles are deleted from the
// managed store; legacy plugins outside the store are only unregistered.
func (l *Launcher) RemovePlugin(id string) error {
	key := strings.ToLower(id)
	l.mu.Lock()
	record := l.plugins[key]
	if record == nil {
		l.mu.Unlock()
		return errors.New("plugin was not found")
	}
	injected := l.gameRunning && l.injectedPID == l.running.PID
	l.mu.Unlock()

	if injected {
		_ = l.client.Unload(record.DLLPath)
	}

	if record.Managed {
		directory := record.BundleRoot
		if directory == "" {
			directory = filepath.Dir(record.DLLPath)
		}
		directory = filepath.Clean(directory)
		if directory == l.storeRoot {
			return errors.New("refusing to delete the managed plugin store itself")
		}
		if relative, err := filepath.Rel(l.storeRoot, directory); err != nil ||
			relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("refusing to delete a plugin outside the managed store")
		}
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("remove plugin files (close Sky and try again): %w", err)
		}
	}

	l.mu.Lock()
	delete(l.plugins, key)
	delete(l.loaded, strings.ToLower(record.DLLPath))
	l.order = slices.DeleteFunc(l.order, func(existing string) bool { return existing == key })
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Plugin removed, but settings could not be saved: " + err.Error())
		return err
	}
	if record.Managed {
		l.setStatus("Plugin and its files were removed.")
	} else {
		l.setStatus("Plugin unregistered; its original file was not deleted.")
	}
	l.refreshLoaded()
	return nil
}

func (l *Launcher) pluginAndInjected(id string) (*pluginRecord, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record := l.plugins[strings.ToLower(id)]
	return record, record != nil && l.gameRunning && l.injectedPID == l.running.PID
}

// PluginUI implements uiserver.Source.
func (l *Launcher) PluginUI(id string) (uiserver.Plugin, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record := l.plugins[strings.ToLower(id)]
	if record == nil || record.UIDir == "" || record.UIEntry == "" {
		return uiserver.Plugin{}, false
	}
	return uiserver.Plugin{
		ID: record.ID, Name: record.Name, Version: record.Version,
		Root: record.UIDir, Entry: record.UIEntry,
	}, true
}

// PluginRPC implements uiserver.Source.
func (l *Launcher) PluginRPC(id string, payload []byte) ([]byte, error) {
	record, injected := l.pluginAndInjected(id)
	if record == nil {
		return nil, errors.New("plugin was not found")
	}
	if !injected {
		return nil, errors.New("Sky is not running with SkyBootstrap yet. Launch Sky from SkyLoader, then try again.")
	}

	response, err := l.client.UI(record.DLLPath, payload)
	if err != nil {
		var remote *uiserver.RemoteError
		if errors.As(err, &remote) {
			return nil, errors.New(remote.Message)
		}
		return nil, fmt.Errorf("plugin request failed: %w", err)
	}
	return response, nil
}

// PluginEvents implements uiserver.Source.
func (l *Launcher) PluginEvents(id string) ([][]byte, error) {
	record, injected := l.pluginAndInjected(id)
	if record == nil || !injected {
		return nil, nil
	}
	events, err := l.client.Poll(record.DLLPath)
	if err != nil {
		var remote *uiserver.RemoteError
		if errors.As(err, &remote) {
			return nil, errors.New(remote.Message)
		}
		return nil, err
	}
	return events, nil
}
