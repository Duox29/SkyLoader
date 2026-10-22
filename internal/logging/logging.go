// Package logging configures the launcher's file logger.
package logging

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

// New opens path for appending and returns a logger plus its close function.
func New(path string) (*log.Logger, func(), error) {
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
