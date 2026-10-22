//go:build !windows

// Package bootstrap is the in-game host loaded into Sky.exe. The real
// implementation is Windows-only; this stub keeps the package buildable on
// other platforms for tests and tooling.
package bootstrap

import "errors"

const apiVersion = 4

var errUnsupported = errors.New("SkyBootstrap is only supported on Windows")

// Start is a no-op on non-Windows platforms.
func Start() {}

// LoadPlugin reports that loading plugins is unsupported.
func LoadPlugin(string) (bool, error) { return false, errUnsupported }

// UnloadPlugin reports that unloading plugins is unsupported.
func UnloadPlugin(string) (bool, error) { return false, errUnsupported }

// PluginCount returns zero on non-Windows platforms.
func PluginCount() uint32 { return 0 }

// APIVersion returns the plugin ABI version implemented by this host.
func APIVersion() uint32 { return apiVersion }

// Logf is a no-op on non-Windows platforms.
func Logf(string, ...any) {}
