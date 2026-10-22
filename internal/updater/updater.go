// Package updater checks GitHub releases and downloads the latest installer.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/HerokeyVN/SkyLoader/internal/versioncmp"
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

// Result describes the outcome of an update check and optional download.
type Result struct {
	Available      bool
	LatestVersion  string
	ReleaseURL     string
	AssetURL       string
	DownloadedPath string
	Message        string
}

// Check queries the latest GitHub release and downloads a newer installer.
func Check(ctx context.Context, currentVersion, userAgent, downloadDir string) (Result, error) {
	result := Result{ReleaseURL: releasePageURL}
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
	result.ReleaseURL = firstNonEmpty(payload.HTMLURL, releasePageURL)
	if result.LatestVersion == "" {
		return result, errors.New("GitHub release has no version tag")
	}
	if versioncmp.Compare(currentVersion, result.LatestVersion) >= 0 {
		result.Message = "SkyLoader is up to date."
		return result, nil
	}

	result.Available = true
	result.AssetURL = SelectAsset(payload.Assets)
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

	filename := "SkyLoader-" + safeVersion(result.LatestVersion) + ".exe"
	destination := filepath.Join(downloadDir, filename)
	if err := download(ctx, result.AssetURL, destination, userAgent); err != nil {
		return result, err
	}
	result.DownloadedPath = destination
	result.Message = "Update downloaded: " + result.LatestVersion
	return result, nil
}

// SelectAsset prefers an installer, then a SkyLoader executable, then any
// downloadable .exe.
func SelectAsset(assets []Asset) string {
	var fallback string
	for _, asset := range assets {
		url := strings.TrimSpace(asset.BrowserDownloadURL)
		if url == "" || !strings.HasSuffix(strings.ToLower(url), ".exe") {
			continue
		}
		name := strings.ToLower(asset.Name + " " + url)
		if strings.Contains(name, "setup") || strings.Contains(name, "installer") {
			return url
		}
		if strings.Contains(name, "skyloader") || strings.Contains(name, "sky-loader") || strings.Contains(name, "sky loader") {
			if fallback == "" {
				fallback = url
			}
		}
		if fallback == "" {
			fallback = url
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
	if err := os.Rename(tempName, destination); err != nil {
		return err
	}
	return nil
}

func safeVersion(version string) string {
	version = strings.TrimSpace(version)
	version = strings.TrimPrefix(strings.TrimPrefix(version, "v"), "V")
	if version == "" {
		return "latest"
	}
	var builder strings.Builder
	for _, character := range version {
		switch {
		case character >= '0' && character <= '9',
			character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '.', character == '-', character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
