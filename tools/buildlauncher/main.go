// Command buildlauncher builds dist/SkyLoader.exe, the Wails desktop app.
//
// The Windows icon and application manifest are compiled into a temporary
// .syso with pure Go, so windres and a C toolchain are not required.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tc-hib/winres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buildlauncher:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		root = flag.String("root", ".", "repository root")
		out  = flag.String("out", "", "output executable path (default <root>/dist/SkyLoader.exe)")
	)
	flag.Parse()

	repository, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(repository, "go.mod")); err != nil {
		return fmt.Errorf("%s does not look like the SkyLoader repository root", repository)
	}
	if *out == "" {
		*out = filepath.Join(repository, "dist", "SkyLoader.exe")
	}
	output, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}

	syso := filepath.Join(repository, "cmd", "skyloader", "rsrc_windows_amd64.syso")
	if err := writeResources(repository, syso); err != nil {
		return err
	}
	defer os.Remove(syso)

	command := exec.Command("go",
		"build",
		"-tags", "desktop,production",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags=-s -w -H=windowsgui",
		"-o", output,
		"./cmd/skyloader",
	)
	command.Dir = repository
	command.Env = withEnv(os.Environ(),
		"GOOS", "windows",
		"GOARCH", "amd64",
		"CGO_ENABLED", "0",
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	info, err := os.Stat(output)
	if err != nil {
		return err
	}
	fmt.Printf("Built %s (%.1f MiB)\n", output, float64(info.Size())/(1<<20))
	return nil
}

func writeResources(repository, destination string) error {
	iconFile, err := os.Open(filepath.Join(repository, "resources", "skyloader.ico"))
	if err != nil {
		return err
	}
	icon, err := winres.LoadICO(iconFile)
	closeErr := iconFile.Close()
	if err != nil {
		return fmt.Errorf("load icon: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}

	manifest, err := os.ReadFile(filepath.Join(repository, "resources", "skyloader.manifest"))
	if err != nil {
		return err
	}

	set := winres.ResourceSet{}
	if err := set.SetIcon(winres.Name("APPICON"), icon); err != nil {
		return fmt.Errorf("add icon: %w", err)
	}
	if err := set.Set(winres.RT_MANIFEST, winres.ID(1), winres.LCIDDefault, manifest); err != nil {
		return fmt.Errorf("add manifest: %w", err)
	}

	file, err := os.Create(destination)
	if err != nil {
		return err
	}
	writeErr := set.WriteObject(file, winres.ArchAMD64)
	closeErr = file.Close()
	if writeErr != nil {
		return fmt.Errorf("write resources: %w", writeErr)
	}
	return closeErr
}

func withEnv(environment []string, pairs ...string) []string {
	excluded := make(map[string]bool, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		excluded[strings.ToUpper(pairs[i])] = true
	}
	result := make([]string, 0, len(environment)+len(pairs)/2)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && excluded[strings.ToUpper(key)] {
			continue
		}
		result = append(result, entry)
	}
	for i := 0; i < len(pairs); i += 2 {
		result = append(result, pairs[i]+"="+pairs[i+1])
	}
	return result
}
