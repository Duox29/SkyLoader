package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	owner          = "HerokeyVN"
	repo           = "SkyLoader"
	releasePageURL = "https://github.com/" + owner + "/" + repo + "/releases"
	maxDownload    = 300 << 20
)

// UpdateView describes the last update check.
type UpdateView struct {
	Checking       bool   `json:"checking"`
	Available      bool   `json:"available"`
	LatestVersion  string `json:"latestVersion,omitempty"`
	ReleaseURL     string `json:"releaseURL,omitempty"`
	DownloadedPath string `json:"downloadedPath,omitempty"`
	Message        string `json:"message,omitempty"`
}

// Asset is one GitHub release asset.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// release mirrors the fields of GitHub's /releases/latest answer.
type release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

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

// checkForUpdates queries the latest GitHub release, compares it with the
// current version, and downloads a newer installer. The returned view is
// also stored in State; a non-nil error always replaces view.Message.
func checkForUpdates(ctx context.Context, currentVersion, userAgent, downloadDir string) (UpdateView, error) {
	view := UpdateView{ReleaseURL: releasePageURL}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+owner+"/"+repo+"/releases/latest", nil)
	if err != nil {
		return view, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return view, fmt.Errorf("connect to GitHub: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		view.Message = "No GitHub release has been published yet."
		return view, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return view, fmt.Errorf("GitHub update check failed (HTTP %d)", response.StatusCode)
	}

	var payload release
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload); err != nil {
		return view, fmt.Errorf("decode GitHub release: %w", err)
	}
	view.LatestVersion = strings.TrimSpace(payload.TagName)
	view.ReleaseURL = payload.HTMLURL
	if view.ReleaseURL == "" {
		view.ReleaseURL = releasePageURL
	}
	if view.LatestVersion == "" {
		return view, errors.New("GitHub release has no version tag")
	}
	if compareVersions(currentVersion, view.LatestVersion) >= 0 {
		view.Message = "SkyLoader is up to date."
		return view, nil
	}

	view.Available = true
	assetURL := selectAsset(payload.Assets)
	if assetURL == "" {
		view.Message = "Update available, but the release has no .exe asset."
		return view, nil
	}
	if downloadDir == "" {
		view.Message = "Update available, but the update folder is unavailable."
		return view, nil
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return view, err
	}

	destination := filepath.Join(downloadDir, "SkyLoader-"+safeVersion(view.LatestVersion)+".exe")
	if err := download(ctx, assetURL, destination, userAgent); err != nil {
		return view, err
	}
	view.DownloadedPath = destination
	view.Message = "Update downloaded: " + view.LatestVersion
	return view, nil
}

// selectAsset prefers an installer, then the first downloadable .exe.
func selectAsset(assets []Asset) string {
	fallback := ""
	for _, asset := range assets {
		url := strings.TrimSpace(asset.BrowserDownloadURL)
		if url == "" || !strings.HasSuffix(strings.ToLower(url), ".exe") {
			continue
		}
		if fallback == "" {
			fallback = url
		}
		name := strings.ToLower(asset.Name + " " + url)
		if strings.Contains(name, "setup") || strings.Contains(name, "installer") {
			return url
		}
	}
	return fallback
}

func download(ctx context.Context, url, destination, userAgent string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", userAgent)
	request.Header.Set("Accept", "application/octet-stream")

	client := &http.Client{Timeout: 10 * time.Minute}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
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
	written, copyErr := io.Copy(temp, io.LimitReader(response.Body, maxDownload+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxDownload {
		return errors.New("update download is too large")
	}
	return os.Rename(tempName, destination)
}

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
