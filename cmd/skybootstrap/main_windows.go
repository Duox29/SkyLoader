//go:build windows

// Command skybootstrap builds SkyBootstrap.dll, the DLL injected into Sky.exe.
//
// The DLL is built with -buildmode=c-shared. Its exported functions below are
// the stable C entry points used by the launcher and by native plugins; the
// named-pipe server that carries most traffic is started automatically when
// the DLL loads.
package main

/*
#include <stdint.h>
#include <wchar.h>
*/
import "C"

import (
	"unsafe"

	"github.com/HerokeyVN/SkyLoader/internal/bootstrap"
	"golang.org/x/sys/windows"
)

func init() {
	// c-shared libraries do not call main, so start the host during package
	// initialization. Start only spawns the pipe loop; DllMain returns at once.
	bootstrap.Start()
}

//export SkyBootstrapStart
func SkyBootstrapStart() C.int {
	bootstrap.Start()
	return 1
}

//export SkyBootstrapLoadPluginW
func SkyBootstrapLoadPluginW(path *C.wchar_t) C.int {
	if path == nil {
		return 0
	}
	pluginPath := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(path)))
	if ok, err := bootstrap.LoadPlugin(pluginPath); err != nil || !ok {
		return 0
	}
	return 1
}

//export SkyBootstrapUnloadPluginW
func SkyBootstrapUnloadPluginW(path *C.wchar_t) C.int {
	if path == nil {
		return 0
	}
	pluginPath := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(path)))
	if ok, err := bootstrap.UnloadPlugin(pluginPath); err != nil || !ok {
		return 0
	}
	return 1
}

//export SkyBootstrapPluginCount
func SkyBootstrapPluginCount() C.uint32_t {
	return C.uint32_t(bootstrap.PluginCount())
}

//export SkyBootstrapApiVersion
func SkyBootstrapApiVersion() C.uint32_t {
	return C.uint32_t(bootstrap.APIVersion())
}

func main() {}
