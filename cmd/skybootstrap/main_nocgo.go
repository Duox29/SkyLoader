//go:build windows && !cgo

// Command skybootstrap needs cgo to build a Windows DLL. This stub keeps
// `go test ./...` and `go vet ./...` working with CGO_ENABLED=0; the real DLL
// build enables cgo and uses the exported functions in main_windows.go.
package main

import "log"

func main() {
	log.Print("skybootstrap requires cgo; build it with tools/buildbootstrap")
}
