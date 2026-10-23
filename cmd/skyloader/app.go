//go:build windows

package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/HerokeyVN/SkyLoader/internal/launcher"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the object bound to the frontend. Wails exposes it as
// window.go.main.App.
type App struct {
	mu       sync.RWMutex
	ctx      context.Context
	launcher *launcher.Launcher
	logger   *log.Logger
}

// NewApp creates the Wails-bound adapter.
func NewApp(core *launcher.Launcher, logger *log.Logger) *App {
	return &App{launcher: core, logger: logger}
}

func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()

	a.launcher.SetEmitter(func(name string, data ...any) {
		runtime.EventsEmit(ctx, name, data...)
	})
	runtime.OnFileDrop(ctx, func(_, _ int, paths []string) {
		if len(paths) == 0 {
			return
		}
		go a.handleDrop(paths)
	})
	go a.autoCheckUpdates()
}

func (a *App) shutdown(context.Context) {
	if err := a.launcher.Close(); err != nil {
		a.logger.Printf("shutdown: %v", err)
	}
}

func (a *App) context() context.Context {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

func (a *App) handleDrop(paths []string) {
	report := a.launcher.Import(paths, false)
	runtime.EventsEmit(a.context(), "import", report)
}

func (a *App) autoCheckUpdates() {
	if _, err := a.launcher.CheckUpdates(a.context()); err != nil {
		a.logger.Printf("automatic update check: %v", err)
	}
}

// GetState returns the complete launcher snapshot.
func (a *App) GetState() launcher.State {
	return a.launcher.State()
}

// GetVersion returns the running launcher version.
func (a *App) GetVersion() string {
	return a.launcher.State().Version
}

// BrowseGame prompts for Sky.exe and saves the choice.
func (a *App) BrowseGame() (string, error) {
	state := a.launcher.State()
	path, err := a.openFileDialog("Locate Sky.exe", filepath.Dir(state.GamePath),
		[]runtime.FileFilter{
			{DisplayName: "Sky executable (Sky.exe)", Pattern: "Sky.exe"},
			{DisplayName: "Executables (*.exe)", Pattern: "*.exe"},
		})
	if err != nil || path == "" {
		return "", err
	}
	if err := a.launcher.SetGamePath(path); err != nil {
		return "", err
	}
	return path, nil
}

// SetGamePath saves a manually entered Sky.exe path.
func (a *App) SetGamePath(path string) error {
	return a.launcher.SetGamePath(path)
}

// BrowseBootstrap prompts for SkyBootstrap.dll and saves the choice.
func (a *App) BrowseBootstrap() (string, error) {
	state := a.launcher.State()
	path, err := a.openFileDialog("Locate SkyBootstrap.dll", filepath.Dir(state.BootstrapPath),
		[]runtime.FileFilter{
			{DisplayName: "SkyBootstrap DLL (*.dll)", Pattern: "*.dll"},
		})
	if err != nil || path == "" {
		return "", err
	}
	if err := a.launcher.SetBootstrapPath(path); err != nil {
		return "", err
	}
	return path, nil
}

// SetBootstrapPath saves a manually entered SkyBootstrap.dll path.
func (a *App) SetBootstrapPath(path string) error {
	return a.launcher.SetBootstrapPath(path)
}

// SetAutoInject toggles automatic injection when Sky.exe starts.
func (a *App) SetAutoInject(enabled bool) error {
	return a.launcher.SetAutoInject(enabled)
}

// Launch starts Sky through Steam or injects into a running instance.
func (a *App) Launch() (string, error) {
	return a.launcher.Launch()
}

// Inject loads SkyBootstrap and enabled plugins into the running game.
func (a *App) Inject() error {
	return a.launcher.Inject()
}

// AddPlugins prompts for one or more plugin files and imports them.
func (a *App) AddPlugins() (launcher.ImportReport, error) {
	paths, err := runtime.OpenMultipleFilesDialog(a.context(), runtime.OpenDialogOptions{
		Title: "Add SkyLoader plugins",
		Filters: []runtime.FileFilter{
			{DisplayName: "Plugin bundles (*.dll, *.zip)", Pattern: "*.dll;*.zip"},
			{DisplayName: "Plugin DLLs (*.dll)", Pattern: "*.dll"},
			{DisplayName: "Plugin bundles (*.zip)", Pattern: "*.zip"},
		},
	})
	if err != nil {
		return launcher.ImportReport{}, err
	}
	if len(paths) == 0 {
		return launcher.ImportReport{Message: ""}, nil
	}
	return a.launcher.Import(paths, false), nil
}

// ImportPaths imports files or folders, optionally replacing conflicts.
func (a *App) ImportPaths(paths []string, force bool) launcher.ImportReport {
	return a.launcher.Import(paths, force)
}

// SetPluginEnabled toggles a plugin.
func (a *App) SetPluginEnabled(id string, enabled bool) error {
	return a.launcher.SetPluginEnabled(id, enabled)
}

// RemovePlugin unregisters a plugin and deletes managed bundle files.
func (a *App) RemovePlugin(id string) error {
	return a.launcher.RemovePlugin(id)
}

// OpenPluginUI returns the local authenticated URL for a plugin UI.
func (a *App) OpenPluginUI(id string) (string, error) {
	return a.launcher.PluginURL(id)
}

// OpenPluginInBrowser opens a plugin UI in the default browser.
func (a *App) OpenPluginInBrowser(id string) error {
	url, err := a.launcher.PluginURL(id)
	if err != nil {
		return err
	}
	return a.launcher.OpenURL(url)
}

// OpenPluginFolder opens the folder that contains a plugin.
func (a *App) OpenPluginFolder(id string) error {
	folder, err := a.launcher.PluginFolder(id)
	if err != nil {
		return err
	}
	return a.launcher.OpenPath(folder)
}

// OpenPluginsFolder opens the managed plugin store.
func (a *App) OpenPluginsFolder() error {
	return a.launcher.OpenPath(a.launcher.State().PluginsDir)
}

// OpenAppFolder opens SkyLoader's local data directory.
func (a *App) OpenAppFolder() error {
	return a.launcher.OpenPath(a.launcher.State().AppDir)
}

// OpenLogs opens the launcher log file.
func (a *App) OpenLogs() error {
	state := a.launcher.State()
	if _, err := os.Stat(state.LogPath); err != nil {
		return a.launcher.OpenPath(state.AppDir)
	}
	return a.launcher.OpenPath(state.LogPath)
}

// OpenExternal opens an http(s) URL in the default browser.
func (a *App) OpenExternal(url string) error {
	return a.launcher.OpenURL(url)
}

// CheckUpdates queries GitHub for a newer release.
func (a *App) CheckUpdates() (launcher.UpdateView, error) {
	ctx, cancel := context.WithTimeout(a.context(), 3*time.Minute)
	defer cancel()
	return a.launcher.CheckUpdates(ctx)
}

// OpenUpdate runs a downloaded installer or opens the release page.
func (a *App) OpenUpdate() error {
	return a.launcher.OpenUpdate()
}

// OpenReleasePage opens the GitHub releases page.
func (a *App) OpenReleasePage() error {
	return a.launcher.OpenURL("https://github.com/HerokeyVN/SkyLoader/releases")
}

func existingDirectory(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ""
	}
	return path
}

func (a *App) openFileDialog(title, directory string, filters []runtime.FileFilter) (string, error) {
	return runtime.OpenFileDialog(a.context(), runtime.OpenDialogOptions{
		Title:            title,
		DefaultDirectory: existingDirectory(directory),
		Filters:          filters,
	})
}
