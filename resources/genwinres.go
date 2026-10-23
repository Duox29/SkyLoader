// Command genwinres compiles the launcher icon and application manifest into a
// Go resource object. It is run by the Makefile; keeping the object generation
// here avoids a separate build-tool command.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tc-hib/winres"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genwinres:", err)
		os.Exit(1)
	}
}

func run() error {
	iconPath := flag.String("icon", "resources/skyloader.ico", "launcher icon")
	manifestPath := flag.String("manifest", "resources/skyloader.manifest", "application manifest")
	output := flag.String("out", "cmd/skyloader/rsrc_windows_amd64.syso", "resource object output")
	flag.Parse()

	iconFile, err := os.Open(*iconPath)
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

	manifest, err := os.ReadFile(*manifestPath)
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

	file, err := os.Create(*output)
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
