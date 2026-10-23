//go:build windows

package main

import (
	"embed"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/HerokeyVN/SkyLoader/internal/launcher"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:web
var embeddedWeb embed.FS

func main() {
	exeDir := executableDir()
	logger, closeLog, err := launcher.NewFileLogger(launcher.LogPath(exeDir))
	if err != nil {
		logger = log.New(os.Stderr, "[SkyLoader] ", log.LstdFlags|log.Lmicroseconds)
		logger.Printf("file logging disabled: %v", err)
		closeLog = func() {}
	}
	defer closeLog()

	core, err := launcher.New(exeDir, launcher.Version, logger)
	if err != nil {
		logger.Printf("startup failed: %v", err)
		showError("SkyLoader could not start", err.Error())
		return
	}

	app := NewApp(core, logger)
	assets, err := fs.Sub(embeddedWeb, "web")
	if err != nil {
		logger.Printf("embedded UI is invalid: %v", err)
		showError("SkyLoader could not start", "The embedded user interface is invalid.")
		return
	}

	err = wails.Run(&options.App{
		Title:     launcher.AppName + " " + launcher.Version,
		Width:     1040,
		Height:    720,
		MinWidth:  760,
		MinHeight: 560,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 8, G: 13, B: 24, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind:             []interface{}{app},
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop:     true,
			DisableWebViewDrop: true,
		},
		Windows: &windows.Options{
			Theme:               windows.SystemDefault,
			WebviewUserDataPath: filepath.Join(launcher.AppDir(exeDir), "webview"),
		},
	})
	if err != nil {
		logger.Printf("wails stopped: %v", err)
		showError("SkyLoader stopped", err.Error())
	}
}

func executableDir() string {
	executable, err := os.Executable()
	if err != nil {
		if directory, dirErr := os.Getwd(); dirErr == nil {
			return directory
		}
		return "."
	}
	return filepath.Dir(executable)
}
