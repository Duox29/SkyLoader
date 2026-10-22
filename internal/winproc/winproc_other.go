//go:build !windows

package winproc

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Process identifies a running game process.
type Process struct {
	PID  uint32
	Path string
}

var errUnsupported = errors.New("Windows process support is only available on Windows")

func NormalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(path)
}

func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func FindProcess(string, string) (Process, bool)    { return Process{}, false }
func InjectDLL(uint32, string, time.Duration) error { return errUnsupported }
func OpenPath(string) error                         { return errUnsupported }
func OpenURL(string) error                          { return errUnsupported }
func DetectSkyPath() string                         { return "" }
