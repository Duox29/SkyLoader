// Package launcher owns SkyLoader's configuration, managed plugin store,
// game-process watcher, plugin injection, local plugin UI bridge, and updates.
package launcher

import (
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Version is the user-visible SkyLoader release version.
const Version = "0.2.0"

// AppName is the product name used in windows and logs.
const AppName = "SkyLoader"

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
