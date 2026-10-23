//go:build windows

// Package launcher owns SkyLoader's configuration, managed plugin store,
// game-process watcher, plugin injection, local plugin UI bridge, and updates.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/HerokeyVN/SkyLoader/internal/uiserver"
)

const (
	// Version is the user-visible SkyLoader release version.
	Version = "0.2.0"
	// AppName is the product name used in windows and logs.
	AppName = "SkyLoader"
)

// PluginView is the frontend-safe description of one registered plugin.
type PluginView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Path        string `json:"path"`
	UIPath      string `json:"uiPath,omitempty"`
	HasUI       bool   `json:"hasUI"`
	Enabled     bool   `json:"enabled"`
	Managed     bool   `json:"managed"`
	Missing     bool   `json:"missing"`
	Loaded      bool   `json:"loaded"`
}

// State is the complete snapshot sent to the frontend.
type State struct {
	Version        string       `json:"version"`
	AppDir         string       `json:"appDir"`
	ConfigPath     string       `json:"configPath"`
	PluginsDir     string       `json:"pluginsDir"`
	LogPath        string       `json:"logPath"`
	GamePath       string       `json:"gamePath"`
	GamePathValid  bool         `json:"gamePathValid"`
	BootstrapPath  string       `json:"bootstrapPath"`
	BootstrapValid bool         `json:"bootstrapValid"`
	AutoInject     bool         `json:"autoInject"`
	GameRunning    bool         `json:"gameRunning"`
	GamePID        uint32       `json:"gamePID,omitempty"`
	Injected       bool         `json:"injected"`
	Plugins        []PluginView `json:"plugins"`
	Update         UpdateView   `json:"update"`
	Status         string       `json:"status"`
}

// ImportItem is one outcome in an import report.
type ImportItem struct {
	Source  string `json:"source"`
	Action  string `json:"action"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Message string `json:"message,omitempty"`
}

// ImportReport tells the frontend what happened and which paths need a
// confirmation before they can be retried with force=true.
type ImportReport struct {
	Paths        []string     `json:"paths"`
	Force        bool         `json:"force"`
	Items        []ImportItem `json:"items"`
	Message      string       `json:"message"`
	NeedsConfirm bool         `json:"needsConfirm"`
}

// Launcher is the launcher's application core. All exported methods are safe
// for concurrent use by the Wails frontend and the local UI server.
type Launcher struct {
	mu       sync.RWMutex
	saveMu   sync.Mutex
	injectMu sync.Mutex
	exeDir   string
	version  string
	cfg      Config
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
	if absolute, err := filepath.Abs(exeDir); err == nil {
		exeDir = absolute
	}

	cfg, status, err := LoadConfig(exeDir)
	if err != nil {
		cfg = Defaults(exeDir)
		status = "Could not read settings: " + err.Error()
		logger.Printf("%s", status)
	}

	storeRoot := PluginDir(exeDir)
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create plugin store: %w", err)
	}

	l := &Launcher{
		exeDir:    exeDir,
		version:   version,
		cfg:       cfg,
		storeRoot: filepath.Clean(storeRoot),
		client:    uiserver.NewClient(),
		logger:    logger,
		plugins:   map[string]*pluginRecord{},
		loaded:    map[string]bool{},
		status:    status,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	l.rebuildRecords()
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
func (l *Launcher) SetEmitter(emit func(name string, data ...any)) {
	l.mu.Lock()
	l.emit = emit
	l.mu.Unlock()
}

func (l *Launcher) notify() {
	l.mu.RLock()
	emit := l.emit
	l.mu.RUnlock()
	if emit != nil {
		emit("state", l.State())
	}
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
	return l.saveConfig("Sky executable path updated.", func(cfg *Config) { cfg.GamePath = path })
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
	return l.saveConfig("SkyBootstrap.dll path updated.", func(cfg *Config) { cfg.BootstrapPath = path })
}

// SetAutoInject controls automatic loading when Sky.exe appears.
func (l *Launcher) SetAutoInject(enabled bool) error {
	status := "Automatic injection disabled."
	if enabled {
		status = "Automatic injection enabled."
	}
	if err := l.saveConfig(status, func(cfg *Config) { cfg.AutoInject = enabled }); err != nil {
		return err
	}
	if enabled {
		l.kickWatcher()
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

	if err := openPath("steam://run/2325290"); err != nil {
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
	return openPath(url)
}

// PluginURL returns the authenticated local URL for a plugin's UI.
func (l *Launcher) PluginURL(id string) (string, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	server := l.server
	l.mu.RUnlock()
	if record == nil {
		return "", errors.New("plugin was not found")
	}
	if record.UIDir == "" || record.UIEntry == "" {
		return "", errors.New("this plugin does not include a web UI")
	}
	if server == nil {
		return "", errors.New("the plugin UI server is not running")
	}
	return server.PluginURL(record.ID), nil
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
	l.update = result
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

func (l *Launcher) saveConfig(status string, edit func(*Config)) error {
	l.mu.Lock()
	edit(&l.cfg)
	l.mu.Unlock()
	if err := l.persist(); err != nil {
		return err
	}
	l.setStatus(status)
	return nil
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

// rebuildRecords is called during construction while the launcher is not
// shared yet.
func (l *Launcher) rebuildRecords() {
	enabledByPath := make(map[string]bool, len(l.cfg.Plugins))
	for _, entry := range l.cfg.Plugins {
		if path := normalizePath(entry.Path); path != "" {
			enabledByPath[strings.ToLower(path)] = entry.Enabled
		}
	}

	l.plugins = map[string]*pluginRecord{}
	l.order = nil

	managedDLLs := map[string]bool{}
	for _, record := range scanBundles(l.storeRoot) {
		if enabled, ok := enabledByPath[strings.ToLower(record.DLLPath)]; ok {
			record.Enabled = enabled
		}
		record.Managed = true
		managedDLLs[strings.ToLower(record.DLLPath)] = true
		l.addRecord(record)
	}

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
	sortPluginOrder(l.order, l.plugins)
}

func (l *Launcher) addRecord(record *pluginRecord) {
	key := strings.ToLower(record.ID)
	if l.plugins[key] != nil {
		l.logger.Printf("ignoring duplicate plugin id %q at %s", record.ID, record.DLLPath)
		return
	}
	l.plugins[key] = record
	l.order = append(l.order, key)
}

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

func (l *Launcher) kickWatcher() {
	select {
	case <-l.stop:
	default:
		go l.pollProcess()
	}
}

// NewFileLogger opens path for appending and returns a logger plus its close
// function.
func NewFileLogger(path string) (*log.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logger := log.New(io.MultiWriter(file, os.Stderr), "[SkyLoader] ",
		log.LstdFlags|log.Lmicroseconds)
	return logger, func() { _ = file.Close() }, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// windowsBaseName returns the final element of a Windows or POSIX path. Plugin
// paths are Windows paths even when tests run on another operating system.
func windowsBaseName(filePath string) string {
	return path.Base(strings.ReplaceAll(filePath, `\`, "/"))
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
