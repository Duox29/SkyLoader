// Package-internal mechanics shared by the launcher's workflow files
// (core.go, config.go, plugins.go, updater.go). Everything here is plain
// plumbing — path and ID normalization, file and archive I/O, plugin ID and
// manifest handling, HTTP requests, and version strings — so the workflow
// files read as short step lists of well-named calls.

package launcher

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ManifestName names a bundle's plugin.json document.
const ManifestName = "plugin.json"

const (
	// maxLength bounds a valid SkyPluginId.
	maxLength = 128
	// maxBundleBytes caps one imported bundle's decompressed payload.
	maxBundleBytes = 300 << 20
	// maxBundleFiles caps one imported bundle's entry count.
	maxBundleFiles = 10000
)

// --- paths and strings -----------------------------------------------

// normalizePath returns a clean absolute Windows path when possible.
func normalizePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "/", `\`))
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

// windowsBaseName returns the final element of a Windows or POSIX path. Plugin
// paths are Windows paths even when tests run on another operating system.
func windowsBaseName(filePath string) string {
	return path.Base(strings.ReplaceAll(filePath, `\`, "/"))
}

// isRegularFile reports whether an ordinary file exists at path.
func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// isHTTPURL reports whether value starts with an http:// or https:// scheme,
// ignoring case. Callers trim surrounding whitespace first.
func isHTTPURL(value string) bool {
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// randomSuffix builds a low-collision directory suffix, or a plain constant
// when the random source is unavailable.
func randomSuffix() string {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err == nil {
		return hex.EncodeToString(raw)
	}
	return "tmp"
}

// --- files and directories ---------------------------------------------

// writeAtomic writes data to destination through a temporary sibling file
// that is renamed into place, so interrupted writes never leave a partial
// configuration behind.
func writeAtomic(pattern, destination string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), pattern)
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, destination)
}

// copyFile overwrites one file, creating parent directories as needed.
func copyFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, maxBundleBytes+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// copyTree duplicates a directory tree, refusing symbolic links and bundles
// that are too large or contain too many files.
func copyTree(source, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	var total int64
	files := 0
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == "." {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle contains a symbolic link: %s", relative)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		files++
		if total > maxBundleBytes {
			return errors.New("bundle is too large")
		}
		if files > maxBundleFiles {
			return errors.New("bundle contains too many files")
		}
		return copyFile(path, target)
	})
}

// moveContents renames every entry of source into destination.
func moveContents(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// NewFileLogger opens path for appending and returns a logger plus its close
// function.
func NewFileLogger(path string) (*log.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logger := log.New(io.MultiWriter(file, os.Stderr), "[SkyLoader] ",
		log.LstdFlags|log.Lmicroseconds)
	return logger, func() { _ = file.Close() }, nil
}

// --- plugin identities -------------------------------------------------

// Canonical returns the lower-case identity when value is a valid SkyPluginId,
// and an empty string otherwise.
func Canonical(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength || strings.Trim(value, ".") == "" {
		return ""
	}
	out := []byte(value)
	for i, character := range out {
		lower, ok := lowerIDByte(character)
		if !ok {
			return ""
		}
		out[i] = lower
	}
	return string(out)
}

// lowerIDByte lower-cases one character of a plugin identity. It reports
// false for characters a SkyPluginId may not contain: anything but ASCII
// letters, digits, '.', '_' and '-'.
func lowerIDByte(character byte) (byte, bool) {
	switch {
	case character >= 'a' && character <= 'z',
		character >= '0' && character <= '9',
		character == '.', character == '_', character == '-':
		return character, true
	case character >= 'A' && character <= 'Z':
		return character + ('a' - 'A'), true
	}
	return 0, false
}

// FromPath derives the legacy filename-based identity used for DLLs without a
// SkyPluginId. A trailing numeric release suffix (my-mod.14.dll) is ignored.
func FromPath(filePath string) string {
	filename := strings.ToLower(windowsBaseName(filePath))
	extension := filepath.Ext(filename)
	stem := strings.TrimSuffix(filename, extension)
	if dot := strings.LastIndexByte(stem, '.'); dot >= 0 &&
		strings.IndexByte(stem, '.') == dot && isDigits(stem[dot+1:]) {
		stem = stem[:dot]
	}
	return stem + extension
}

// isDigits reports whether value is a non-empty run of ASCII digits.
func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

// DirectoryName converts an identity into a safe directory name. Valid IDs are
// already safe; legacy identities are sanitized and hashed when necessary.
//
// Names that Win32 would reinterpret fall back to the hashed branch: a
// trailing dot collapses to a different path, and device names such as CON or
// NUL are reserved by the filesystem. Canonical identities are lower-case.
func DirectoryName(id string) string {
	if canonical := Canonical(id); canonical != "" && len(canonical) <= 80 &&
		!strings.HasSuffix(canonical, ".") {
		stem := canonical
		if dot := strings.IndexByte(stem, '.'); dot >= 0 {
			stem = stem[:dot]
		}
		switch stem {
		case "con", "prn", "aux", "nul",
			"com1", "com2", "com3", "com4", "com5", "com6", "com7", "com8", "com9",
			"lpt1", "lpt2", "lpt3", "lpt4", "lpt5", "lpt6", "lpt7", "lpt8", "lpt9":
		default:
			return canonical
		}
	}

	sanitized := make([]byte, 0, 64)
	for i := 0; i < len(id) && len(sanitized) < 64; i++ {
		lower, ok := lowerIDByte(id[i])
		if !ok {
			lower = '_'
		}
		sanitized = append(sanitized, lower)
	}
	if len(sanitized) == 0 {
		sanitized = []byte("plugin")
	}
	sum := sha1.Sum([]byte(id))
	return string(sanitized) + "-" + hex.EncodeToString(sum[:4])
}

// DisplayName returns a readable fallback for a plugin without a PluginName.
func DisplayName(filePath string) string {
	base := windowsBaseName(filePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Manifest describes a plugin bundle. All paths are relative to the bundle
// root. The DLL's VERSIONINFO remains the source of truth for a SkyPluginId;
// the manifest may repeat it for convenience. readVersionInfo also carries its
// VERSIONINFO-derived metadata in this struct, without path fields.
// It lives beside the manifest helpers in this file because they own the
// plugin.json contract.
type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Version     string `json:"version,omitempty"`
	Author      string `json:"author,omitempty"`
	Description string `json:"description,omitempty"`
	DLL         string `json:"dll,omitempty"`
	UI          string `json:"ui,omitempty"`
	UIEntry     string `json:"uiEntry,omitempty"`
}

// --- manifests and bundles ----------------------------------------------

// readManifest reads and normalizes the plugin.json document of the bundle
// rooted at root. A missing manifest yields an empty value.
func readManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, nil
	}
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse %s: %w", ManifestName, err)
	}
	for _, field := range []struct {
		name  string
		value *string
	}{
		{"ui", &manifest.UI},
		{"uiEntry", &manifest.UIEntry},
		{"dll", &manifest.DLL},
	} {
		if *field.value == "" {
			continue
		}
		// Declared paths must stay inside the bundle: trim Windows-style
		// separators, then reject absolute and escaping paths.
		relative := strings.TrimSpace(strings.ReplaceAll(*field.value, "\\", "/"))
		clean := filepath.Clean(filepath.FromSlash(relative))
		if strings.HasPrefix(relative, "/") || (len(relative) >= 2 && relative[1] == ':') ||
			clean == "." || !filepath.IsLocal(clean) {
			return Manifest{}, fmt.Errorf("manifest %s: must be a relative path inside the bundle", field.name)
		}
		*field.value = filepath.ToSlash(clean)
	}
	return manifest, nil
}

// findDLL locates the bundle's plugin DLL: the manifest path, the single DLL
// next to the manifest, or the DLL matching the bundle directory's name.
func findDLL(root string, manifest Manifest) (string, error) {
	if manifest.DLL != "" {
		path := filepath.Join(root, filepath.FromSlash(manifest.DLL))
		if !isRegularFile(path) {
			return "", fmt.Errorf("bundle DLL %q was not found", manifest.DLL)
		}
		return path, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	var dlls []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			dlls = append(dlls, entry.Name())
		}
	}
	switch len(dlls) {
	case 0:
		return "", errors.New("bundle has no DLL")
	case 1:
		return filepath.Join(root, dlls[0]), nil
	default:
		base := strings.ToLower(filepath.Base(root))
		for _, name := range dlls {
			if strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name))) == base {
				return filepath.Join(root, name), nil
			}
		}
		return "", errors.New("bundle has multiple DLLs; set \"dll\" in plugin.json")
	}
}

// resolveUI maps a bundle's UI declaration to the UI directory and its entry
// relative to that directory. Bundles without a UI return empty strings.
func resolveUI(root string, manifest Manifest) (string, string, error) {
	uiDir := manifest.UI
	if uiDir == "" {
		if isRegularFile(filepath.Join(root, "ui", "index.html")) {
			uiDir = "ui"
		} else {
			return "", "", nil
		}
	}
	absolute := filepath.Join(root, filepath.FromSlash(uiDir))
	if info, err := os.Stat(absolute); err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("bundle UI directory %q was not found", uiDir)
	}

	entry := manifest.UIEntry
	if entry == "" {
		entry = filepath.ToSlash(filepath.Join(uiDir, "index.html"))
	} else if !strings.HasPrefix(filepath.ToSlash(entry), filepath.ToSlash(uiDir)+"/") {
		// uiEntry may be relative to the UI directory or to the bundle root.
		entry = filepath.ToSlash(filepath.Join(uiDir, entry))
	}
	entryPath := filepath.Join(root, filepath.FromSlash(entry))
	if !isRegularFile(entryPath) {
		return "", "", fmt.Errorf("bundle UI entry %q was not found", entry)
	}
	relativeEntry, err := filepath.Rel(absolute, entryPath)
	if err != nil {
		return "", "", err
	}
	return absolute, filepath.ToSlash(relativeEntry), nil
}

// resolveBundleID picks a plugin identity from the manifest, the DLL's
// VERSIONINFO, or the legacy filename, and rejects manifest/DLL disagreement.
func resolveBundleID(manifest, versionInfo Manifest, dllPath string) (string, error) {
	id := Canonical(manifest.ID)
	if manifest.ID != "" && id == "" {
		return "", fmt.Errorf("invalid bundle id %q", manifest.ID)
	}
	if id == "" {
		id = versionInfo.ID
	}
	if id == "" {
		id = FromPath(dllPath)
	}
	if manifest.ID != "" && versionInfo.ID != "" && id != versionInfo.ID {
		return "", fmt.Errorf("bundle id %q does not match DLL SkyPluginId %q", id, versionInfo.ID)
	}
	return id, nil
}

// sidecarUIDirectory finds a legacy UI directory sitting next to a standalone
// DLL: "<stem>.ui" is preferred over a generic "ui" sibling.
func sidecarUIDirectory(dllPath string) string {
	directory := filepath.Dir(dllPath)
	stem := strings.TrimSuffix(filepath.Base(dllPath), filepath.Ext(dllPath))
	for _, candidate := range []string{
		filepath.Join(directory, stem+".ui"),
		filepath.Join(directory, "ui"),
	} {
		if isRegularFile(filepath.Join(candidate, "index.html")) {
			return candidate
		}
	}
	return ""
}

// resolveBundleRoot finds the directory that hosts plugin.json or a DLL: the
// passed directory itself, or its single child directory when an archive
// wrapped the bundle in a folder.
func resolveBundleRoot(directory string) (string, error) {
	if isRegularFile(filepath.Join(directory, ManifestName)) {
		return directory, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	var directories []string
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, filepath.Join(directory, entry.Name()))
		}
	}
	if len(directories) == 1 {
		child := directories[0]
		if isRegularFile(filepath.Join(child, ManifestName)) || hasDLL(child) {
			return child, nil
		}
	}
	if hasDLL(directory) {
		return directory, nil
	}
	return "", errors.New("bundle has no plugin.json or DLL")
}

// hasDLL reports whether any regular file below directory ends in ".dll".
func hasDLL(directory string) bool {
	found := false
	_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// --- archives -------------------------------------------------------------

// extractZip extracts a bundle zip with traversal and zip-bomb protection.
func extractZip(source, destination string) error {
	reader, err := zip.OpenReader(source)
	if err != nil {
		return fmt.Errorf("open bundle zip: %w", err)
	}
	defer reader.Close()

	var total int64
	files := 0
	for _, entry := range reader.File {
		name := filepath.Clean(filepath.FromSlash(strings.ReplaceAll(entry.Name, "\\", "/")))
		if name == "." || !filepath.IsLocal(name) {
			return fmt.Errorf("bundle contains an unsafe path: %q", entry.Name)
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle contains a symbolic link: %q", entry.Name)
		}

		target := filepath.Join(destination, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}

		total += int64(entry.UncompressedSize64)
		files++
		if total > maxBundleBytes {
			return errors.New("bundle is too large")
		}
		if files > maxBundleFiles {
			return errors.New("bundle contains too many files")
		}
		if err := copyZipEntry(entry, target); err != nil {
			return err
		}
	}
	return nil
}

// copyZipEntry writes one zip member. Its parent directory is created before
// the member is opened, as folder imports do.
func copyZipEntry(entry *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// --- versions and HTTP ------------------------------------------------------

// compareVersions returns -1 when a is older than b, 0 when they are
// equivalent, and 1 when a is newer. Numeric segments compare numerically;
// after those, a pre-release-style label compares lexicographically.
func compareVersions(a, b string) int {
	first, firstLabel := parseVersion(a)
	second, secondLabel := parseVersion(b)
	for i := 0; i < len(first) || i < len(second); i++ {
		var x, y uint64
		if i < len(first) {
			x = first[i]
		}
		if i < len(second) {
			y = second[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return strings.Compare(firstLabel, secondLabel)
}

// parseVersion splits a version into numeric segments, skipping the v/V
// prefix and separator characters, plus the label after the first letter.
func parseVersion(version string) ([]uint64, string) {
	version = strings.TrimSpace(version)
	if len(version) > 0 && (version[0] == 'v' || version[0] == 'V') {
		version = version[1:]
	}
	var numbers []uint64
	for i := 0; i < len(version); {
		character := version[i]
		switch {
		case character >= '0' && character <= '9':
			end := i
			for end < len(version) && version[end] >= '0' && version[end] <= '9' {
				end++
			}
			number, _ := strconv.ParseUint(version[i:end], 10, 64)
			numbers = append(numbers, number)
			i = end
		case character == '.' || character == '-' || character == '_' ||
			character == '+' || character == ' ' || character == '\t':
			i++
		default:
			return numbers, strings.ToLower(version[i:])
		}
	}
	return numbers, ""
}

// safeVersion maps a release tag onto a file-name-safe segment.
func safeVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	if version == "" {
		return "latest"
	}
	return strings.Map(func(character rune) rune {
		switch {
		case character >= '0' && character <= '9',
			character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '.', character == '-', character == '_':
			return character
		default:
			return '_'
		}
	}, version)
}

// httpGet issues one bounded GET and returns the open response; the caller
// reads and closes the body. The label names transport failures, e.g.
// "connect to GitHub: ...".
func httpGet(ctx context.Context, label, url, userAgent, accept string,
	headers [][2]string, timeout time.Duration) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", accept)
	for _, pair := range headers {
		request.Header.Set(pair[0], pair[1])
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", label, err)
	}
	return response, nil
}

// decodeJSON decodes a JSON document whose size is bounded by limit.
func decodeJSON(body io.Reader, limit int64, value any) error {
	return json.NewDecoder(io.LimitReader(body, limit)).Decode(value)
}

// encodeJSON renders value as the two-space-indented, newline-terminated JSON
// used for every file SkyLoader writes (config.json and staged plugin.json).
func encodeJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// maxDownload caps the size of one downloaded release installer.
const maxDownload = 300 << 20

// downloadFile streams url into destination through a sibling ".update-*.part"
// temporary file that is renamed into place, so an interrupted download never
// leaves a partial installer at destination.
func downloadFile(ctx context.Context, url, destination, userAgent string, maxBytes int64) error {
	response, err := httpGet(ctx, "download update", url, userAgent,
		"application/octet-stream", nil, 10*time.Minute)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download update failed (HTTP %d)", response.StatusCode)
	}

	temp, err := os.CreateTemp(filepath.Dir(destination), ".update-*.part")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	written, copyErr := io.Copy(temp, io.LimitReader(response.Body, maxBytes+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxBytes {
		return errors.New("update download is too large")
	}
	return os.Rename(tempName, destination)
}
