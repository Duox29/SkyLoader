//go:build !windows

package main

import "log"

func showError(title, message string) {
	log.Printf("%s: %s", title, message)
}
