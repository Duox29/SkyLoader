package updater

import "testing"

func TestSelectAssetPrefersInstaller(t *testing.T) {
	assets := []Asset{
		{Name: "SkyLoader.exe", BrowserDownloadURL: "https://example/SkyLoader.exe"},
		{Name: "SkyLoader-Setup-0.2.0.exe", BrowserDownloadURL: "https://example/SkyLoader-Setup-0.2.0.exe"},
	}
	if got := SelectAsset(assets); got != "https://example/SkyLoader-Setup-0.2.0.exe" {
		t.Fatalf("SelectAsset = %q", got)
	}
}

func TestSelectAssetFallsBackToExe(t *testing.T) {
	assets := []Asset{
		{Name: "notes.txt", BrowserDownloadURL: "https://example/notes.txt"},
		{Name: "mod.exe", BrowserDownloadURL: "https://example/mod.exe"},
	}
	if got := SelectAsset(assets); got != "https://example/mod.exe" {
		t.Fatalf("SelectAsset = %q", got)
	}
}

func TestSafeVersion(t *testing.T) {
	if got := safeVersion("v1.2.3 beta"); got != "1.2.3_beta" {
		t.Fatalf("safeVersion = %q", got)
	}
}
