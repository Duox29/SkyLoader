//go:build windows

package main

import "golang.org/x/sys/windows"

func showError(title, message string) {
	titlePtr, _ := windows.UTF16PtrFromString(title)
	messagePtr, _ := windows.UTF16PtrFromString(message)
	_, _ = windows.MessageBox(0, messagePtr, titlePtr, windows.MB_OK|windows.MB_ICONERROR)
}
