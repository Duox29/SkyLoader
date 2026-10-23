//go:build windows

// Plugin lifecycle: discovering bundles, importing files into the managed
// store, and toggling/removing installed plugins. Low-level mechanics stay in
// utils.go so each function here reads as a short step list.

package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/HerokeyVN/SkyLoader/internal/uiserver"
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

// --- bundle loading ------------------------------------------------------

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
	versionInfo, _ := readVersionInfo(dllPath)

	id, err := resolveBundleID(manifest, versionInfo, dllPath)
	if err != nil {
		return nil, err
	}
	uiDir, uiEntry, err := resolveUI(root, manifest)
	if err != nil {
		return nil, err
	}
	return &pluginRecord{
		ID:          id,
		Name:        firstNonEmpty(manifest.Name, versionInfo.Name, DisplayName(dllPath)),
		Version:     firstNonEmpty(manifest.Version, versionInfo.Version),
		Author:      firstNonEmpty(manifest.Author, versionInfo.Author),
		Description: firstNonEmpty(manifest.Description, versionInfo.Description),
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

// recordForDLL builds the plugin record for a registered DLL outside the
// managed store: a bundle sibling if one exists, else VERSIONINFO metadata,
// else a filename-derived identity with an optional sidecar UI directory.
func recordForDLL(path string) *pluginRecord {
	if bundle, ok := bundleForDLL(path); ok {
		return bundle
	}
	record := &pluginRecord{ID: FromPath(path), DLLPath: path, Enabled: true}
	if info, err := readVersionInfo(path); err == nil {
		record.ID = info.ID
		record.Name = info.Name
		record.Version = info.Version
		record.Author = info.Author
		record.Description = info.Description
	}
	// Legacy plugins may keep their UI next to the DLL instead of inside a
	// plugin.json bundle.
	if candidate := sidecarUIDirectory(path); candidate != "" {
		record.UIDir, record.UIEntry = candidate, "index.html"
	}
	return record
}

// scanBundles loads every bundle directory of the managed store, skipping
// staging leftovers of interrupted imports.
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

// --- importing -----------------------------------------------------------
//
// importPlugin installs a DLL, a zip bundle, or an unpacked bundle directory
// into the managed store. Missing destinations are added as-is; existing ones
// go through the conflict policy and are replaced by renaming the staged copy
// into place after the old directory is parked in a sibling .old- directory
// (locked files on Windows cannot be deleted in place, but an empty directory
// can be renamed).
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

	if err := stageSource(input, stage, info); err != nil {
		return importError(err)
	}
	bundle, err := loadBundle(stage)
	if err != nil {
		return importError(err)
	}

	// keepStage reports that the stage moved into place and must not be
	// removed by the deferred cleanup.
	var result Result
	destination := filepath.Join(root, DirectoryName(bundle.ID))
	if _, err := os.Stat(destination); err != nil {
		result, keepStage = adoptBundleStage(stage, destination, bundle)
	} else {
		result, keepStage = replaceBundle(root, stage, destination, bundle, force)
	}
	return result
}

// errUnsupportedPlugin rejects inputs that are neither a directory, a zip
// bundle, nor a DLL.
var errUnsupportedPlugin = errors.New("Drop a .dll, a .zip bundle, or a bundle folder.")

// stageSource materializes the input inside the staging directory: bundle
// directories are copied as-is, zips are unpacked and flattened when an
// archive wrapped the bundle in a folder, and lone DLLs are staged together
// with an optional sidecar UI directory.
func stageSource(input, stage string, info os.FileInfo) error {
	switch {
	case info.IsDir():
		resolved, err := resolveBundleRoot(input)
		if err != nil {
			return err
		}
		return copyTree(resolved, stage)
	case strings.EqualFold(filepath.Ext(input), ".zip"):
		if err := extractZip(input, stage); err != nil {
			return err
		}
		resolved, err := resolveBundleRoot(stage)
		if err != nil {
			return err
		}
		if resolved == stage {
			return nil
		}
		return moveContents(resolved, stage)
	case strings.EqualFold(filepath.Ext(input), ".dll"):
		return stageDLL(input, stage)
	default:
		return errUnsupportedPlugin
	}
}

// stageDLL copies a single DLL plus an optional sidecar UI directory into the
// staging directory and writes the manifest that describes the staged bundle.
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

	if candidate := sidecarUIDirectory(source); candidate != "" {
		if err := copyTree(candidate, filepath.Join(stage, "ui")); err != nil {
			return err
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

	data, err := encodeJSON(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stage, ManifestName), data, 0o644)
}

// adoptBundleStage moves a never-before-installed bundle into the store.
func adoptBundleStage(stage, destination string, bundle *pluginRecord) (Result, bool) {
	if err := os.Rename(stage, destination); err != nil {
		return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}, false
	}
	return pluginResult(ActionAdded, bundle, "Plugin added."), true
}

// replaceBundle reconciles a staged import with what is already installed.
// The conflict policy may decline the file; otherwise the old directory is
// parked aside and the staged copy takes its place.
func replaceBundle(root, stage, destination string, bundle *pluginRecord, force bool) (Result, bool) {
	existing, loadErr := loadBundle(destination)
	if loadErr == nil && !force {
		if declined, ok := conflictPolicy(existing, bundle); ok {
			return declined, false
		}
	}

	old := filepath.Join(root, ".old-"+randomSuffix())
	if err := os.Rename(destination, old); err != nil {
		return Result{
			Action: ActionFailed, ID: bundle.ID,
			Message: "The installed plugin is in use. Close Sky and try again.",
		}, false
	}
	if err := os.Rename(stage, destination); err != nil {
		_ = os.Rename(old, destination)
		return Result{Action: ActionFailed, ID: bundle.ID, Message: err.Error()}, false
	}
	_ = os.RemoveAll(old)
	return pluginResult(ActionUpdated, bundle, "Plugin updated."), true
}

// conflictPolicy decides whether an import must stop before replacing an
// existing installation: a different plugin, an older build, or disagreeing
// metadata declines it; false lets the replacement proceed.
func conflictPolicy(existing, bundle *pluginRecord) (Result, bool) {
	switch {
	case existing.ID != bundle.ID:
		return pluginResult(ActionConflict, bundle,
			fmt.Sprintf("The destination already contains plugin %s.", existing.ID)), true
	case existing.Version != "" && bundle.Version != "":
		if compareVersions(bundle.Version, existing.Version) < 0 {
			return pluginResult(ActionUnchanged, bundle,
				fmt.Sprintf("Newer %s version %s is already installed.", existing.Name, existing.Version)), true
		}
	case (existing.Version == "") != (bundle.Version == ""):
		return pluginResult(ActionConflict, bundle,
			"Installed and incoming builds disagree about version metadata. Replace it?"), true
	case !strings.EqualFold(filepath.Base(existing.DLLPath), filepath.Base(bundle.DLLPath)):
		return pluginResult(ActionConflict, bundle,
			"A plugin with the same name is already installed. Replace it?"), true
	}
	return Result{}, false
}

func pluginResult(action Action, bundle *pluginRecord, message string) Result {
	return Result{Action: action, ID: bundle.ID, Name: bundle.Name, Version: bundle.Version, Message: message}
}

func importError(err error) Result {
	return Result{Action: ActionFailed, Message: err.Error()}
}

// --- launcher-facing lifecycle -------------------------------------------

// Import installs DLLs, zip bundles, or unpacked bundle directories into the
// managed store. A conflicting bundle is returned as an item with action
// "conflict"; retry the same call with force=true after confirmation.
func (l *Launcher) Import(paths []string, force bool) ImportReport {
	report := ImportReport{Paths: append([]string(nil), paths...), Force: force}
	var tally importTally

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
		tally.count(result.Action)
		if result.Action == ActionAdded || result.Action == ActionUpdated {
			l.afterInstall(result, source)
		}
	}

	report.Message, report.NeedsConfirm = tally.summarize(len(report.Items))
	if report.Message != "" {
		l.Notice("info", report.Message)
	}
	return report
}

// importTally counts import outcomes and renders the report message.
type importTally struct {
	added, updated, conflicts, failed int
}

func (t *importTally) count(action Action) {
	switch action {
	case ActionAdded:
		t.added++
	case ActionUpdated:
		t.updated++
	case ActionConflict:
		t.conflicts++
	case ActionUnchanged:
	default:
		t.failed++
	}
}

func (t *importTally) summarize(itemCount int) (message string, needsConfirm bool) {
	if itemCount == 0 {
		return "No plugin files were selected.", false
	}
	switch {
	case t.conflicts > 0:
		message = "One or more plugins already exist. Confirm replacement to continue."
		needsConfirm = true
	case t.added+t.updated > 0:
		message = fmt.Sprintf("Imported %d plugin(s): %d added, %d updated.", t.added+t.updated, t.added, t.updated)
	case itemCount > 0:
		message = "No plugin files were changed."
	}
	if t.failed > 0 {
		if message != "" {
			message += " "
		}
		message += fmt.Sprintf("%d plugin(s) could not be imported.", t.failed)
	}
	return message, needsConfirm
}

// afterInstall registers the installed bundle, persists it, and — when Sky is
// already injected with SkyBootstrap — asks the game to load it at once.
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
	injected := l.injectedLocked()
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
	injected := l.injectedLocked()
	l.mu.Unlock()

	if err := l.persist(); err != nil {
		l.setStatus("Could not save the plugin setting: " + err.Error())
		return err
	}

	if injected {
		l.applyEnabledToSky(dllPath, enabled)
	} else {
		l.setStatus("Plugin setting saved.")
	}
	l.notify()
	return nil
}

// applyEnabledToSky loads or unloads a plugin in the injected game and reports
// the outcome.
func (l *Launcher) applyEnabledToSky(dllPath string, enabled bool) {
	var pipeErr error
	if enabled {
		pipeErr = l.client.Load(dllPath)
	} else {
		pipeErr = l.client.Unload(dllPath)
	}
	switch {
	case pipeErr != nil:
		l.setStatus("Setting saved, but Sky did not accept it: " + pipeErr.Error())
	case enabled:
		l.setStatus("Plugin loaded into Sky.")
	default:
		l.setStatus("Plugin unloaded from Sky.")
	}
	l.refreshLoaded()
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
	injected := l.injectedLocked()
	l.mu.Unlock()

	if injected {
		_ = l.client.Unload(record.DLLPath)
	}
	if err := l.removeManagedBundle(record); err != nil {
		return err
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

// removeManagedBundle deletes the directory of a managed bundle. It refuses
// anything that is not safely within the managed plugin store.
func (l *Launcher) removeManagedBundle(record *pluginRecord) error {
	if !record.Managed {
		return nil
	}
	directory := filepath.Clean(record.folder())
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
	return nil
}

// --- plugin registry --------------------------------------------------------

// pluginRecord is one registered plugin: either a managed bundle in the store
// or a legacy DLL registered by absolute path.
type pluginRecord struct {
	ID          string
	Name        string
	Version     string
	Author      string
	Description string
	DLLPath     string
	BundleRoot  string
	UIDir       string
	UIEntry     string
	Enabled     bool
	Managed     bool
	Missing     bool
}

// hasUI reports whether the plugin ships a web UI the launcher can serve.
func (r *pluginRecord) hasUI() bool {
	return r.UIDir != "" && r.UIEntry != ""
}

// folder returns the plugin's bundle root, or the DLL's directory for legacy
// plugins that are not bundles.
func (r *pluginRecord) folder() string {
	if r.BundleRoot != "" {
		return r.BundleRoot
	}
	return filepath.Dir(r.DLLPath)
}

// rebuildRecords rehydrates the plugin registry from the managed store and
// the saved settings. It runs during construction while the launcher is not
// shared yet.
func (l *Launcher) rebuildRecords() {
	enabledByPath := enabledStateByPath(l.cfg.Plugins)
	l.plugins, l.order = map[string]*pluginRecord{}, nil

	managedDLLs := l.loadManagedRecords(enabledByPath)
	l.loadRegisteredRecords(enabledByPath, managedDLLs)
	sortPluginOrder(l.order, l.plugins)
}

// recordsLocked returns the registered plugins in display order. The caller
// must hold l.mu.
func (l *Launcher) recordsLocked() []*pluginRecord {
	records := make([]*pluginRecord, 0, len(l.order))
	for _, key := range l.order {
		if record := l.plugins[key]; record != nil {
			records = append(records, record)
		}
	}
	return records
}

// addRecord registers a record by its lower-case identity, ignoring
// duplicates.
func (l *Launcher) addRecord(record *pluginRecord) {
	key := strings.ToLower(record.ID)
	if l.plugins[key] != nil {
		l.logger.Printf("ignoring duplicate plugin id %q at %s", record.ID, record.DLLPath)
		return
	}
	l.plugins[key] = record
	l.order = append(l.order, key)
}

// sortPluginOrder orders identities alphabetically by display name, then ID.
func sortPluginOrder(order []string, plugins map[string]*pluginRecord) {
	name := func(id string) string {
		record := plugins[id]
		if record == nil {
			return ""
		}
		return strings.ToLower(firstNonEmpty(record.Name, record.ID))
	}
	slices.SortStableFunc(order, func(a, b string) int {
		if compared := strings.Compare(name(a), name(b)); compared != 0 {
			return compared
		}
		return strings.Compare(strings.ToLower(plugins[a].ID), strings.ToLower(plugins[b].ID))
	})
}

// --- plugin UI bridge --------------------------------------------------------

func (l *Launcher) pluginAndInjected(id string) (*pluginRecord, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record := l.plugins[strings.ToLower(id)]
	return record, record != nil && l.injectedLocked()
}

// PluginUI implements uiserver.Source.
func (l *Launcher) PluginUI(id string) (uiserver.Plugin, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	record := l.plugins[strings.ToLower(id)]
	if record == nil || !record.hasUI() {
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

// --- record rehydration (called during construction) ---------------------------

// enabledStateByPath maps lower-case DLL paths onto their saved enabled state.
func enabledStateByPath(entries []PluginEntry) map[string]bool {
	enabled := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if path := normalizePath(entry.Path); path != "" {
			enabled[strings.ToLower(path)] = entry.Enabled
		}
	}
	return enabled
}

// loadManagedRecords registers bundles from the managed store and returns the
// set of lower-case DLL paths it registered.
func (l *Launcher) loadManagedRecords(enabledByPath map[string]bool) map[string]bool {
	managedDLLs := map[string]bool{}
	for _, record := range scanBundles(l.storeRoot) {
		if enabled, ok := enabledByPath[strings.ToLower(record.DLLPath)]; ok {
			record.Enabled = enabled
		}
		record.Managed = true
		managedDLLs[strings.ToLower(record.DLLPath)] = true
		l.addRecord(record)
	}
	return managedDLLs
}

// loadRegisteredRecords registers the remaining DLLs named by the settings,
// warning about paths that no longer exist.
func (l *Launcher) loadRegisteredRecords(enabledByPath, managedDLLs map[string]bool) {
	for _, entry := range l.cfg.Plugins {
		path := normalizePath(entry.Path)
		if path == "" || managedDLLs[strings.ToLower(path)] {
			continue
		}
		if !isRegularFile(path) {
			l.logger.Printf("registered plugin is missing: %s", path)
			continue
		}
		record := recordForDLL(path)
		record.Enabled = entry.Enabled
		if record.Name == "" {
			record.Name = DisplayName(path)
		}
		l.addRecord(record)
	}
}
