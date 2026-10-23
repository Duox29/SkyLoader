// Update workflow: check GitHub for a newer release and download its
// installer. Version-string parsing, HTTP plumbing, and the download-to-temp
// mechanics live in utils.go.
package launcher

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	owner          = "HerokeyVN"
	repo           = "SkyLoader"
	releasePageURL = "https://github.com/" + owner + "/" + repo + "/releases"
	releasesAPIURL = "https://api.github.com/repos/" + owner + "/" + repo + "/releases/latest"
)

// errNoRelease means GitHub answered 404: nothing has been published yet.
var errNoRelease = errors.New("no release published yet")

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

// checkForUpdates queries the latest GitHub release, compares it with the
// current version, and downloads a newer installer. The returned view is
// also stored in State; a non-nil error always replaces view.Message.
func checkForUpdates(ctx context.Context, currentVersion, userAgent, downloadDir string) (UpdateView, error) {
	view := UpdateView{ReleaseURL: releasePageURL}

	payload, err := fetchLatestRelease(ctx, userAgent)
	// GitHub answers 404 until the first release is published.
	if errors.Is(err, errNoRelease) {
		view.Message = "No GitHub release has been published yet."
		return view, nil
	}
	if err != nil {
		return view, err
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

	return downloadInstaller(ctx, view, payload, userAgent, downloadDir)
}

// fetchLatestRelease issues the GitHub query and decodes its answer. Only a
// 404 is swallowed, returning errNoRelease; everything else surfaces as an
// error.
func fetchLatestRelease(ctx context.Context, userAgent string) (release, error) {
	response, err := httpGet(ctx, "connect to GitHub", releasesAPIURL, userAgent,
		"application/vnd.github+json",
		[][2]string{{"X-GitHub-Api-Version", "2022-11-28"}}, 30*time.Second)
	if err != nil {
		return release{}, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return release{}, errNoRelease
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return release{}, fmt.Errorf("GitHub update check failed (HTTP %d)", response.StatusCode)
	}
	var payload release
	if err := decodeJSON(response.Body, 8<<20, &payload); err != nil {
		return release{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	return payload, nil
}

// downloadInstaller picks the release's installer asset and downloads it.
func downloadInstaller(ctx context.Context, view UpdateView, payload release, userAgent, downloadDir string) (UpdateView, error) {
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
	if err := downloadFile(ctx, assetURL, destination, userAgent, maxDownload); err != nil {
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
