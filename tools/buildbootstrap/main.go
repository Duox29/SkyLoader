// Command buildbootstrap builds SkyBootstrap.dll for Windows/amd64.
//
// Go requires cgo to produce a Windows DLL, so the tool provisions a portable
// Zig toolchain and uses it as the C compiler. No system C toolchain, MinGW,
// or Visual Studio installation is needed. The downloaded archive is verified
// against the SHA-256 checksum published in Zig's download index.
package main

import (
	"archive/tar"
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"
)

const (
	zigVersion    = "0.13.0"
	zigIndexURL   = "https://ziglang.org/download/index.json"
	windowsTarget = "x86_64-windows-gnu"
)

type options struct {
	root  string
	out   string
	zig   string
	cache string
}

type zigRelease struct {
	Tarball string `json:"tarball"`
	Shasum  string `json:"shasum"`
}

type hostPlatform struct {
	key string
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "buildbootstrap:", err)
		os.Exit(1)
	}
}

func run() error {
	var opts options
	flag.StringVar(&opts.root, "root", ".", "repository root")
	flag.StringVar(&opts.out, "out", "", "output DLL path (default <root>/dist/SkyBootstrap.dll)")
	flag.StringVar(&opts.zig, "zig", "", "path to an existing Zig executable")
	flag.StringVar(&opts.cache, "cache", "", "toolchain cache directory")
	flag.Parse()

	root, err := filepath.Abs(opts.root)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("%s does not look like the SkyLoader repository root", root)
	}
	if opts.out == "" {
		opts.out = filepath.Join(root, "dist", "SkyBootstrap.dll")
	}
	out, err := filepath.Abs(opts.out)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}

	zig, err := resolveZig(opts)
	if err != nil {
		return err
	}
	fmt.Println("Using", zig)

	command := exec.Command("go",
		"build",
		"-buildmode=c-shared",
		"-trimpath",
		"-buildvcs=false",
		"-ldflags=-s -w",
		"-o", out,
		"./cmd/skybootstrap",
	)
	command.Dir = root
	command.Env = withEnv(os.Environ(),
		"GOOS", "windows",
		"GOARCH", "amd64",
		"CGO_ENABLED", "1",
		"CC", quoteArg(zig)+" cc -target "+windowsTarget,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// cgo writes a companion header for c-shared builds. The documented ABI
	// header lives in libraries/SkyBootstrap/include; avoid shipping a second,
	// generated header with the installer.
	_ = os.Remove(strings.TrimSuffix(out, filepath.Ext(out)) + ".h")

	info, err := os.Stat(out)
	if err != nil {
		return err
	}
	fmt.Printf("Built %s (%.1f MiB)\n", out, float64(info.Size())/(1<<20))
	return nil
}

func resolveZig(opts options) (string, error) {
	explicit := opts.zig
	if explicit == "" {
		explicit = strings.TrimSpace(os.Getenv("SKYLOADER_ZIG"))
	}
	if explicit != "" {
		absolute, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		if info, err := os.Stat(absolute); err == nil && info.IsDir() {
			absolute = filepath.Join(absolute, zigExecutableName())
		}
		if _, err := os.Stat(absolute); err != nil {
			return "", fmt.Errorf("Zig executable not found: %s", absolute)
		}
		return absolute, nil
	}

	platform, err := detectHost()
	if err != nil {
		return "", err
	}
	cache, err := cacheDirectory(opts.cache)
	if err != nil {
		return "", err
	}
	installDir := filepath.Join(cache, "zig-"+zigVersion+"-"+platform.key)
	executable := filepath.Join(installDir, zigExecutableName())
	if isFile(executable) {
		return executable, nil
	}

	fmt.Printf("Downloading Zig %s for %s...\n", zigVersion, platform.key)
	if err := installZig(cache, installDir, platform); err != nil {
		return "", err
	}
	if !isFile(executable) {
		return "", fmt.Errorf("Zig was installed but %s is missing", executable)
	}
	return executable, nil
}

func detectHost() (hostPlatform, error) {
	var key string
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		key = "x86_64-windows"
	case "linux/amd64":
		key = "x86_64-linux"
	case "linux/arm64":
		key = "aarch64-linux"
	case "darwin/amd64":
		key = "x86_64-macos"
	case "darwin/arm64":
		key = "aarch64-macos"
	default:
		return hostPlatform{}, fmt.Errorf("automatic Zig setup does not support %s/%s; set SKYLOADER_ZIG", runtime.GOOS, runtime.GOARCH)
	}
	return hostPlatform{key: key}, nil
}

func cacheDirectory(flagValue string) (string, error) {
	directory := strings.TrimSpace(flagValue)
	if directory == "" {
		directory = strings.TrimSpace(os.Getenv("SKYLOADER_CACHE"))
	}
	if directory == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("find cache directory: %w", err)
		}
		directory = filepath.Join(userCache, "SkyLoader", "build")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return "", err
	}
	return absolute, nil
}

func installZig(cache, installDir string, platform hostPlatform) error {
	release, err := fetchRelease(platform.key)
	if err != nil {
		return err
	}
	archiveName := path.Base(release.Tarball)
	if archiveName == "." || archiveName == "/" || archiveName == "" {
		return fmt.Errorf("unexpected Zig archive URL %q", release.Tarball)
	}
	archivePath := filepath.Join(cache, archiveName)

	if !isFile(archivePath) || !matchesChecksum(archivePath, release.Shasum) {
		if err := download(archivePath, release.Tarball, release.Shasum); err != nil {
			return err
		}
	}

	temp, err := os.MkdirTemp(cache, ".zig-extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)

	switch {
	case strings.EqualFold(filepath.Ext(archivePath), ".zip"):
		err = extractZip(archivePath, temp)
	case strings.HasSuffix(archivePath, ".tar.xz"):
		err = extractTarXZ(archivePath, temp)
	default:
		err = fmt.Errorf("unsupported Zig archive format: %s", filepath.Base(archivePath))
	}
	if err != nil {
		return fmt.Errorf("extract Zig: %w", err)
	}

	executable, err := findZigExecutable(temp)
	if err != nil {
		return err
	}
	extractedRoot := filepath.Dir(executable)
	if extractedRoot != temp {
		// Keep the toolchain self-contained when the archive nests it.
		_ = os.RemoveAll(installDir)
		if err := os.Rename(extractedRoot, installDir); err != nil {
			return err
		}
		return nil
	}
	_ = os.RemoveAll(installDir)
	return os.Rename(temp, installDir)
}

func fetchRelease(hostKey string) (zigRelease, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	response, err := client.Get(zigIndexURL)
	if err != nil {
		return zigRelease{}, fmt.Errorf("download Zig index: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return zigRelease{}, fmt.Errorf("download Zig index: HTTP %s", response.Status)
	}
	var index map[string]map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&index); err != nil {
		return zigRelease{}, fmt.Errorf("parse Zig index: %w", err)
	}
	versions := index[zigVersion]
	if versions == nil {
		return zigRelease{}, fmt.Errorf("Zig %s is not listed in the download index", zigVersion)
	}
	raw, ok := versions[hostKey]
	if !ok {
		return zigRelease{}, fmt.Errorf("Zig %s has no download for %s", zigVersion, hostKey)
	}
	var release zigRelease
	if err := json.Unmarshal(raw, &release); err != nil {
		return zigRelease{}, fmt.Errorf("parse Zig %s download for %s: %w", zigVersion, hostKey, err)
	}
	if release.Tarball == "" || release.Shasum == "" {
		return zigRelease{}, fmt.Errorf("Zig %s has no download for %s", zigVersion, hostKey)
	}
	return release, nil
}

func download(destination, url, expected string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("download Zig archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Zig archive: HTTP %s", response.Status)
	}

	temp, err := os.CreateTemp(filepath.Dir(destination), ".zig-download-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temp, hash), response.Body)
	closeErr := temp.Close()
	if copyErr != nil {
		return fmt.Errorf("download Zig archive: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("Zig archive checksum mismatch: got %s, want %s", actual, expected)
	}
	// Windows does not replace an existing destination with os.Rename.
	_ = os.Remove(destination)
	if err := os.Rename(tempName, destination); err != nil {
		return err
	}
	fmt.Printf("Downloaded %.1f MiB\n", float64(written)/(1<<20))
	return nil
}

func extractZip(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive contains a symbolic link: %s", entry.Name)
		}
		open := func() (io.ReadCloser, error) { return entry.Open() }
		if err := writeArchiveEntry(destination, entry.Name, entry.FileInfo().IsDir(), entry.Mode(), open); err != nil {
			return err
		}
	}
	return nil
}

func extractTarXZ(archive, destination string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	reader, err := xz.NewReader(file)
	if err != nil {
		return err
	}
	tarReader := tar.NewReader(reader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir, tar.TypeReg, tar.TypeRegA:
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive contains a link: %s", header.Name)
		default:
			continue
		}
		mode := fs.FileMode(header.Mode).Perm()
		open := func() (io.ReadCloser, error) { return io.NopCloser(tarReader), nil }
		if err := writeArchiveEntry(destination, header.Name, header.Typeflag == tar.TypeDir, mode, open); err != nil {
			return err
		}
	}
}

func writeArchiveEntry(destination, name string, directory bool, mode fs.FileMode, open func() (io.ReadCloser, error)) error {
	cleaned := path.Clean(strings.ReplaceAll(name, `\`, "/"))
	if cleaned == "." {
		return nil
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) < 2 {
		// Skip top-level archive files such as documentation.
		return nil
	}
	relative := path.Join(parts[1:]...)
	if relative == "" || relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("archive entry escapes the destination: %s", name)
	}
	target := filepath.Join(destination, filepath.FromSlash(relative))
	if !inside(destination, target) {
		return fmt.Errorf("archive entry escapes the destination: %s", name)
	}
	if directory {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := open()
	if err != nil {
		return err
	}
	defer input.Close()
	if mode == 0 {
		mode = 0o644
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm()|0o200)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func findZigExecutable(root string) (string, error) {
	var found string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == zigExecutableName() {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("Zig executable was not found in the downloaded archive")
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(found, 0o755)
	}
	return found, nil
}

func matchesChecksum(path, expected string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expected)
}

func inside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func zigExecutableName() string {
	if runtime.GOOS == "windows" {
		return "zig.exe"
	}
	return "zig"
}

func quoteArg(value string) string {
	if strings.ContainsAny(value, " \t") {
		return `"` + value + `"`
	}
	return value
}

// withEnv replaces environment variables instead of appending duplicates,
// which keeps GOOS/GOARCH and CC deterministic across hosts.
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
