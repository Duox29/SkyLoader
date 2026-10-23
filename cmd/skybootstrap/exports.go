//go:build windows && cgo

// The exported functions below are the stable C entry points used by native
// plugins and external tools. The named-pipe server is started when the DLL
// loads; these functions are a secondary interface.
package main

/*
#include <stdint.h>
#include <wchar.h>
*/
import "C"

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func init() {
	// c-shared libraries do not call main, so start the host during package
	// initialization. startHost only spawns the pipe loop; DllMain returns at
	// once.
	startHost()
}

//export SkyBootstrapStart
func SkyBootstrapStart() C.int {
	startHost()
	return 1
}

//export SkyBootstrapLoadPluginW
func SkyBootstrapLoadPluginW(path *C.wchar_t) C.int {
	if path == nil {
		return 0
	}
	pluginPath := windows.UTF16PtrToString((*uint16)(unsafe.Pointer(path)))
	if ok, err := loadPlugin(pluginPath); err != nil || !ok {
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
	if ok, err := unloadPlugin(pluginPath); err != nil || !ok {
		return 0
	}
	return 1
}

//export SkyBootstrapPluginCount
func SkyBootstrapPluginCount() C.uint32_t {
	return C.uint32_t(pluginCount())
}

//export SkyBootstrapApiVersion
func SkyBootstrapApiVersion() C.uint32_t {
	return C.uint32_t(apiVersion)
}
