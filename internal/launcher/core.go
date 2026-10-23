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
	"path/filepath"
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

const (
	readyStatus  = "Ready. Start Sky from Steam or click Launch Sky."
	skySteamURL  = "steam://run/2325290"
	skyImageName = "Sky.exe"
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
	if status == "" {
		status = readyStatus
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

// emitter returns the installed frontend callback, or nil before one is set.
func (l *Launcher) emitter() func(name string, data ...any) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.emit
}

func (l *Launcher) notify() {
	if emit := l.emitter(); emit != nil {
		emit("state", l.State())
	}
}

// Notice sends a transient frontend notification.
func (l *Launcher) Notice(kind, message string) {
	if message == "" {
		return
	}
	if emit := l.emitter(); emit != nil {
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
		Injected:       l.injectedLocked(),
		Update:         l.update,
		Status:         l.status,
		Plugins:        make([]PluginView, 0, len(l.order)),
	}
	for _, record := range l.recordsLocked() {
		state.Plugins = append(state.Plugins, pluginView(record, l.loaded[strings.ToLower(record.DLLPath)]))
	}
	return state
}

// pluginView describes a registered plugin for the frontend.
func pluginView(record *pluginRecord, loaded bool) PluginView {
	return PluginView{
		ID:          record.ID,
		Name:        record.Name,
		Version:     record.Version,
		Author:      record.Author,
		Description: record.Description,
		Path:        record.DLLPath,
		UIPath:      record.UIDir,
		HasUI:       record.hasUI(),
		Enabled:     record.Enabled,
		Managed:     record.Managed,
		Missing:     record.Missing,
		Loaded:      loaded,
	}
}

// injectedLocked reports whether SkyBootstrap is loaded into the running
// game. The caller must hold l.mu.
func (l *Launcher) injectedLocked() bool {
	return l.gameRunning && l.injectedPID == l.running.PID
}

// --- settings -------------------------------------------------------------

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

// saveConfig applies an edit to the settings and persists them.
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

// persist writes the current settings, including the plugin registry, to
// config.json.
func (l *Launcher) persist() error {
	l.saveMu.Lock()
	defer l.saveMu.Unlock()

	l.mu.RLock()
	cfg := l.cfg
	records := l.recordsLocked()
	entries := make([]PluginEntry, 0, len(records))
	for _, record := range records {
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

// --- launch and injection -----------------------------------------------------

// Launch starts Sky through Steam, or injects into an already running game.
func (l *Launcher) Launch() (string, error) {
	if process, running := l.findGame(); running {
		l.setPending(true)
		l.injectAsync(process)
		return "Sky.exe is already running; loading SkyBootstrap.", nil
	}

	if err := openPath(skySteamURL); err != nil {
		return "", fmt.Errorf("could not open Steam: %w", err)
	}
	l.setPending(true)
	l.setStatus("Steam was asked to start Sky. Waiting for Sky.exe...")
	return "Waiting for Steam to start Sky.exe...", nil
}

// Inject loads SkyBootstrap and the enabled plugins into the running game.
func (l *Launcher) Inject() error {
	process, running := l.findGame()
	if !running {
		return errors.New("Sky.exe is not running")
	}
	l.setPending(false)
	l.injectAsync(process)
	return nil
}

// gamePath returns the configured Sky.exe path.
func (l *Launcher) gamePath() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg.GamePath
}

// findGame looks for the running Sky process, preferring the configured path.
func (l *Launcher) findGame() (Process, bool) {
	return findProcess(skyImageName, l.gamePath())
}

// setPending records whether a launch request is waiting for Sky to appear.
func (l *Launcher) setPending(pending bool) {
	l.mu.Lock()
	l.pending = pending
	l.mu.Unlock()
}

// --- opening files, folders, and links ------------------------------------------

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
	if !isHTTPURL(url) {
		return errors.New("only http and https links are allowed")
	}
	return openPath(url)
}

// --- plugin URLs and folders --------------------------------------------------------

// PluginURL returns the authenticated local URL for a plugin's UI.
func (l *Launcher) PluginURL(id string) (string, error) {
	l.mu.RLock()
	record := l.plugins[strings.ToLower(id)]
	server := l.server
	l.mu.RUnlock()
	if record == nil {
		return "", errors.New("plugin was not found")
	}
	if !record.hasUI() {
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
	return record.folder(), nil
}

// --- updates ------------------------------------------------------------------------

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
		url = releasePageURL
	}
	return l.OpenURL(url)
}

// --- watcher plumbing ----------------------------------------------------------------

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
