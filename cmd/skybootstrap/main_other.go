//go:build !windows

// Command skybootstrap is only available on Windows. This stub keeps
// `go build ./...` and `go test ./...` working on other platforms.
package main

import "log"

func main() {
	log.Print("skybootstrap can only be built for Windows")
}
