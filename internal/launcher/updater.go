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

// Asset is one GitHub release asset.
type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

// UpdateResult describes the outcome of an update check and optional download.
type UpdateResult struct {
	Available      bool
	LatestVersion  string
	ReleaseURL     string
	AssetURL       string
	DownloadedPath string
	Message        string
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

// checkForUpdates queries the latest GitHub release and downloads a newer installer.
func checkForUpdates(ctx context.Context, currentVersion, userAgent, downloadDir string) (UpdateResult, error) {
	result := UpdateResult{ReleaseURL: releasePageURL}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+owner+"/"+repo+"/releases/latest", nil)
	if err != nil {
		return result, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", userAgent)

	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return result, fmt.Errorf("connect to GitHub: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		result.Message = "No GitHub release has been published yet."
		return result, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, fmt.Errorf("GitHub update check failed (HTTP %d)", response.StatusCode)
	}

	var payload release
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&payload); err != nil {
		return result, fmt.Errorf("decode GitHub release: %w", err)
	}
	result.LatestVersion = strings.TrimSpace(payload.TagName)
	result.ReleaseURL = payload.HTMLURL
	if result.ReleaseURL == "" {
		result.ReleaseURL = releasePageURL
	}
	if result.LatestVersion == "" {
		return result, errors.New("GitHub release has no version tag")
	}
	if compareVersions(currentVersion, result.LatestVersion) >= 0 {
		result.Message = "SkyLoader is up to date."
		return result, nil
	}

	result.Available = true
	result.AssetURL = selectAsset(payload.Assets)
	if result.AssetURL == "" {
		result.Message = "Update available, but the release has no .exe asset."
		return result, nil
	}
	if downloadDir == "" {
		result.Message = "Update available, but the update folder is unavailable."
		return result, nil
	}
	if err := os.MkdirAll(downloadDir, 0o755); err != nil {
		return result, err
	}

	destination := filepath.Join(downloadDir, "SkyLoader-"+safeVersion(result.LatestVersion)+".exe")
	if err := download(ctx, result.AssetURL, destination, userAgent); err != nil {
		return result, err
	}
	result.DownloadedPath = destination
	result.Message = "Update downloaded: " + result.LatestVersion
	return result, nil
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
