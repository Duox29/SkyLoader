//go:build windows

package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/HerokeyVN/SkyLoader/internal/uiserver"
)

// Launcher is the launcher's application core. All exported methods are safe
// for concurrent use by the Wails frontend and the local UI server.
type Launcher struct {
	mu       sync.RWMutex
	saveMu   sync.Mutex
	injectMu sync.Mutex
	exeDir   string
	version  string
	cfg      Config
	store    *Store
	server   *uiserver.Server
	client   *uiserver.Client
	logger   *log.Logger

	emit      func(name string, data ...any)
	storeRoot string

	plugins map[string]*pluginRecord
	order   []string
	loaded  map[string]bool
	status  string

	gameRunning  bool
	running      Process
	injectedPID  uint32
	attemptedPID uint32
	pending      bool

	injectingPID uint32
	update       UpdateView

	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
}

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

// New loads settings, discovers the managed plugin store, starts the local UI
// server, and begins watching for Sky.exe.
func New(exeDir, version string, logger *log.Logger) (*Launcher, error) {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	absolute, err := filepath.Abs(exeDir)
	if err == nil {
		exeDir = absolute
	}

	cfg, migrationStatus, loadErr := LoadConfig(exeDir)
	if loadErr != nil {
		cfg = Defaults(exeDir)
		migrationStatus = "Could not read settings: " + loadErr.Error()
		logger.Printf("%s", migrationStatus)
	}

	store, err := NewStore(PluginDir(exeDir))
	if err != nil {
		return nil, fmt.Errorf("create plugin store: %w", err)
	}

	l := &Launcher{
		exeDir:    exeDir,
		version:   version,
		cfg:       cfg,
		store:     store,
		storeRoot: filepath.Clean(store.Root),
		client:    uiserver.NewClient(),
		logger:    logger,
		plugins:   map[string]*pluginRecord{},
		loaded:    map[string]bool{},
		status:    migrationStatus,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	l.rebuildRecordsLocked()
	if l.status == "" {
		l.status = "Ready. Start Sky from Steam or click Launch Sky."
	}

	server, err := uiserver.New(l)
	if err != nil {
		return nil, fmt.Errorf("start plugin UI server: %w", err)
	}
	l.server = server
	go l.watch()
	return l, nil
}

// Close stops the watcher and the local UI server.
func (l *Launcher) Close() error {
	l.stopOnce.Do(func() { close(l.stop) })
	<-l.done
	if l.server != nil {
		return l.server.Close()
	}
	return nil
}

// SetEmitter installs the event callback used to push state to the frontend.
// It may be called before or after New; no events are emitted until it is set.
func (l *Launcher) SetEmitter(emit func(name string, data ...any)) {
	l.mu.Lock()
	l.emit = emit
	l.mu.Unlock()
}

func (l *Launcher) notify() {
	l.mu.RLock()
	emit := l.emit
	l.mu.RUnlock()
	if emit == nil {
		return
	}
	emit("state", l.State())
}

// Notice sends a transient frontend notification.
func (l *Launcher) Notice(kind, message string) {
	if message == "" {
		return
	}
	l.mu.RLock()
	emit := l.emit
	l.mu.RUnlock()
	if emit != nil {
		emit("notice", map[string]string{"kind": kind, "message": message})
	}
}

// State returns a consistent snapshot of the launcher.
func (l *Launcher) State() State {
	l.mu.RLock()
	defer l.mu.RUnlock()

	state := State{
		Version:        l.version,
		AppDir:         AppDir(l.exeDir),
		ConfigPath:     ConfigPath(l.exeDir),
		PluginsDir:     PluginDir(l.exeDir),
		LogPath:        LogPath(l.exeDir),
		GamePath:       l.cfg.GamePath,
		GamePathValid:  isRegularFile(l.cfg.GamePath),
		BootstrapPath:  l.cfg.BootstrapPath,
		BootstrapValid: isRegularFile(l.cfg.BootstrapPath),
		AutoInject:     l.cfg.AutoInject,
		GameRunning:    l.gameRunning,
		GamePID:        l.running.PID,
		Injected:       l.gameRunning && l.injectedPID == l.running.PID,
		Update:         l.update,
		Status:         l.status,
		Plugins:        make([]PluginView, 0, len(l.order)),
	}
	for _, id := range l.order {
		record := l.plugins[id]
		if record == nil {
			continue
		}
		state.Plugins = append(state.Plugins, PluginView{
			ID:          record.ID,
			Name:        record.Name,
			Version:     record.Version,
			Author:      record.Author,
			Description: record.Description,
			Path:        record.DLLPath,
			UIPath:      record.UIDir,
			HasUI:       record.UIDir != "" && record.UIEntry != "",
			Enabled:     record.Enabled,
			Managed:     record.Managed,
			Missing:     record.Missing,
			Loaded:      l.loaded[strings.ToLower(record.DLLPath)],
		})
	}
	return state
}

// SetGamePath normalizes and persists a new Sky.exe path.
func (l *Launcher) SetGamePath(path string) error {
	path = normalizePath(path)
	if path == "" {
		return errors.New("Sky executable path is empty")
	}
	l.mu.Lock()
	l.cfg.GamePath = path
	l.mu.Unlock()
	if err := l.persist(); err != nil {
		return err
	}
	l.setStatus("Sky executable path updated.")
	return nil
}

// SetBootstrapPath normalizes and persists a new SkyBootstrap.dll path.
func (l *Launcher) SetBootstrapPath(path string) error {
	path = normalizePath(path)
	if path == "" {
		return errors.New("SkyBootstrap.dll path is empty")
	}
	if !isRegularFile(path) {
		return errors.New("the selected SkyBootstrap.dll does not exist")
	}
	l.mu.Lock()
	l.cfg.BootstrapPath = path
	l.mu.Unlock()
	if err := l.persist(); err != nil {
		return err
	}
	l.setStatus("SkyBootstrap.dll path updated.")
	return nil
}

// SetAutoInject controls automatic loading when Sky.exe appears.
func (l *Launcher) SetAutoInject(enabled bool) error {
	l.mu.Lock()
	l.cfg.AutoInject = enabled
	l.mu.Unlock()
	if err := l.persist(); err != nil {
		return err
	}
	if enabled {
		l.setStatus("Automatic injection enabled.")
		l.kickWatcher()
	} else {
		l.setStatus("Automatic injection disabled.")
	}
	return nil
}

// Launch starts Sky through Steam, or injects into an already running game.
func (l *Launcher) Launch() (string, error) {
	l.mu.RLock()
	gamePath := l.cfg.GamePath
	l.mu.RUnlock()

	process, running := findProcess("Sky.exe", gamePath)
	if running {
		l.mu.Lock()
		l.pending = true
		l.mu.Unlock()
		l.injectAsync(process)
		return "Sky.exe is already running; loading SkyBootstrap.", nil
	}

	if err := openURL("steam://run/2325290"); err != nil {
		return "", fmt.Errorf("could not open Steam: %w", err)
	}
	l.mu.Lock()
	l.pending = true
	l.mu.Unlock()
	l.setStatus("Steam was asked to start Sky. Waiting for Sky.exe...")
	return "Waiting for Steam to start Sky.exe...", nil
}

// Inject loads SkyBootstrap and the enabled plugins into the running game.
func (l *Launcher) Inject() error {
	l.mu.RLock()
	gamePath := l.cfg.GamePath
	l.mu.RUnlock()

	process, running := findProcess("Sky.exe", gamePath)
	if !running {
		return errors.New("Sky.exe is not running")
	}
	l.mu.Lock()
	l.pending = false
	l.mu.Unlock()
	l.injectAsync(process)
	return nil
}

// OpenPath opens a file or folder in its default Windows handler.
func (l *Launcher) OpenPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path is empty")
	}
	return openPath(path)
}

// OpenURL opens a validated http(s) URL.
func (l *Launcher) OpenURL(url string) error {
	url = strings.TrimSpace(url)
	if !strings.HasPrefix(strings.ToLower(url), "http://") &&
		!strings.HasPrefix(strings.ToLower(url), "https://") {
		return errors.New("only http and https links are allowed")
	}
	return openURL(url)
}

// PluginURL returns the authenticated local URL for a plugin's UI.
func (l *Launcher) PluginURL(id string) (string, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	l.mu.RUnlock()
	if record == nil {
		return "", errors.New("plugin was not found")
	}
	if record.UIDir == "" || record.UIEntry == "" {
		return "", errors.New("this plugin does not include a web UI")
	}
	if l.server == nil {
		return "", errors.New("the plugin UI server is not running")
	}
	return l.server.PluginURL(record.ID), nil
}

// PluginFolder returns the folder containing a plugin's bundle or DLL.
func (l *Launcher) PluginFolder(id string) (string, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	l.mu.RUnlock()
	if record == nil {
		return "", errors.New("plugin was not found")
	}
	if record.BundleRoot != "" {
		return record.BundleRoot, nil
	}
	return filepath.Dir(record.DLLPath), nil
}

// CheckUpdates queries GitHub and downloads an installer when a newer release
// exists. The returned view is also stored in State.
func (l *Launcher) CheckUpdates(ctx context.Context) (UpdateView, error) {
	l.mu.Lock()
	if l.update.Checking {
		view := l.update
		l.mu.Unlock()
		return view, nil
	}
	l.update.Checking = true
	l.mu.Unlock()
	l.notify()

	result, err := checkForUpdates(ctx, l.version, "SkyLoader/"+l.version, UpdatesDir(l.exeDir))

	l.mu.Lock()
	l.update = UpdateView{
		Available:      result.Available,
		LatestVersion:  result.LatestVersion,
		ReleaseURL:     result.ReleaseURL,
		DownloadedPath: result.DownloadedPath,
		Message:        result.Message,
	}
	if err != nil {
		l.update.Message = err.Error()
	}
	view := l.update
	l.mu.Unlock()
	l.notify()
	return view, err
}

// OpenUpdate starts the downloaded installer, or opens the release page.
func (l *Launcher) OpenUpdate() error {
	l.mu.RLock()
	view := l.update
	l.mu.RUnlock()
	if view.DownloadedPath != "" && isRegularFile(view.DownloadedPath) {
		return openPath(view.DownloadedPath)
	}
	url := view.ReleaseURL
	if url == "" {
		url = "https://github.com/HerokeyVN/SkyLoader/releases"
	}
	return l.OpenURL(url)
}

// GamePath returns the configured Sky.exe path.
func (l *Launcher) GamePath() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg.GamePath
}

func (l *Launcher) setStatus(message string) {
	if message == "" {
		return
	}
	l.mu.Lock()
	l.status = message
	l.mu.Unlock()
	l.logger.Printf("%s", message)
	l.notify()
}

func (l *Launcher) emitNotice(kind, message string) {
	l.Notice(kind, message)
}

func (l *Launcher) persist() error {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()

	l.mu.RLock()
	cfg := l.cfg
	entries := make([]PluginEntry, 0, len(l.order))
	for _, key := range l.order {
		record := l.plugins[key]
		if record == nil {
			continue
		}
		entries = append(entries, PluginEntry{
			Path:    record.DLLPath,
			UIPath:  record.UIDir,
			Enabled: record.Enabled,
		})
	}
	l.mu.RUnlock()
	cfg.Plugins = entries
	if err := cfg.Save(ConfigPath(l.exeDir)); err != nil {
		l.logger.Printf("save settings: %v", err)
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}

// rebuildRecordsLocked is called during construction while the launcher is not
// shared yet.
func (l *Launcher) rebuildRecordsLocked() {
	enabledByPath := make(map[string]bool, len(l.cfg.Plugins))
	for _, entry := range l.cfg.Plugins {
		path := normalizePath(entry.Path)
		if path != "" {
			enabledByPath[strings.ToLower(path)] = entry.Enabled
		}
	}

	l.plugins = map[string]*pluginRecord{}
	l.order = nil

	add := func(record *pluginRecord) {
		key := strings.ToLower(record.ID)
		if existing := l.plugins[key]; existing != nil {
			l.logger.Printf("ignoring duplicate plugin id %q at %s", record.ID, record.DLLPath)
			return
		}
		l.plugins[key] = record
		l.order = append(l.order, key)
	}

	managedDLLs := map[string]bool{}
	for _, bundle := range l.store.Scan() {
		record := &pluginRecord{
			ID:          bundle.ID,
			Name:        firstNonEmpty(bundle.Name, DisplayName(bundle.DLLPath)),
			Version:     bundle.Version,
			Author:      bundle.Author,
			Description: bundle.Description,
			DLLPath:     bundle.DLLPath,
			BundleRoot:  bundle.Root,
			UIDir:       bundle.UIDir,
			UIEntry:     bundle.UIEntry,
			Enabled:     true,
			Managed:     true,
		}
		if enabled, ok := enabledByPath[strings.ToLower(bundle.DLLPath)]; ok {
			record.Enabled = enabled
		}
		managedDLLs[strings.ToLower(bundle.DLLPath)] = true
		add(record)
	}

	for _, entry := range l.cfg.Plugins {
		path := normalizePath(entry.Path)
		if path == "" {
			continue
		}
		if managedDLLs[strings.ToLower(path)] {
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
		add(record)
	}

	sort.SliceStable(l.order, func(i, j int) bool {
		first := l.plugins[l.order[i]]
		second := l.plugins[l.order[j]]
		firstKey := strings.ToLower(firstNonEmpty(first.Name, first.ID))
		secondKey := strings.ToLower(firstNonEmpty(second.Name, second.ID))
		if firstKey == secondKey {
			return strings.ToLower(first.ID) < strings.ToLower(second.ID)
		}
		return firstKey < secondKey
	})
}

func recordForDLL(path string) *pluginRecord {
	record := &pluginRecord{
		ID:      FromPath(path),
		DLLPath: path,
		Enabled: true,
	}
	if bundle, ok := LoadForDLL(path); ok {
		record.ID = bundle.ID
		record.Name = bundle.Name
		record.Version = bundle.Version
		record.Author = bundle.Author
		record.Description = bundle.Description
		record.BundleRoot = bundle.Root
		record.UIDir = bundle.UIDir
		record.UIEntry = bundle.UIEntry
		return record
	}
	if info, err := readVersionInfo(path); err == nil {
		record.ID = info.ID
		record.Name = info.Name
		record.Version = info.Version
		record.Author = info.Author
		record.Description = info.Description
	}
	record.UIDir, record.UIEntry = FindUI(path)
	return record
}

func (l *Launcher) record(id string) *pluginRecord {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.plugins[strings.ToLower(id)]
}

func (l *Launcher) setLoaded(paths []string) {
	loaded := make(map[string]bool, len(paths))
	for _, path := range paths {
		loaded[strings.ToLower(normalizePath(path))] = true
	}
	l.mu.Lock()
	l.loaded = loaded
	l.mu.Unlock()
}

func (l *Launcher) refreshLoaded() {
	paths, err := l.client.List()
	if err != nil {
		return
	}
	l.setLoaded(paths)
	l.notify()
}

func (l *Launcher) isInjected() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.gameRunning && l.injectedPID == l.running.PID
}

func (l *Launcher) kickWatcher() {
	select {
	case <-l.stop:
	default:
		go l.pollProcess()
	}
}
