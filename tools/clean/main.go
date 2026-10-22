// Command clean removes local build outputs for the Go rewrite.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	root, err := filepath.Abs(".")
	if err != nil {
		fail(err)
	}
	targets := []string{
		filepath.Join(root, "dist"),
		filepath.Join(root, "cmd", "skyloader", "rsrc_windows_amd64.syso"),
		filepath.Join(root, "cmd", "skyloader", "skyloader_windows_amd64.syso"),
	}
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			fail(err)
		}
	}
	fmt.Println("Removed build outputs.")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "clean:", err)
	os.Exit(1)
}
