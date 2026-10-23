package launcher

import "testing"

func TestSelectAssetPrefersInstaller(t *testing.T) {
	assets := []Asset{
		{Name: "SkyLoader.exe", BrowserDownloadURL: "https://example/SkyLoader.exe"},
		{Name: "SkyLoader-Setup-0.2.0.exe", BrowserDownloadURL: "https://example/SkyLoader-Setup-0.2.0.exe"},
	}
	if got := selectAsset(assets); got != "https://example/SkyLoader-Setup-0.2.0.exe" {
		t.Fatalf("selectAsset = %q", got)
	}
}

func TestSelectAssetFallsBackToExe(t *testing.T) {
	assets := []Asset{
		{Name: "notes.txt", BrowserDownloadURL: "https://example/notes.txt"},
		{Name: "mod.exe", BrowserDownloadURL: "https://example/mod.exe"},
	}
	if got := selectAsset(assets); got != "https://example/mod.exe" {
		t.Fatalf("selectAsset = %q", got)
	}
}

func TestSafeVersion(t *testing.T) {
	if got := safeVersion("v1.2.3 beta"); got != "1.2.3_beta" {
		t.Fatalf("safeVersion = %q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2", "1.10", -1},
		{"2.0.0-beta", "2.0.0-alpha", 1},
		{"1.4.0", "1.4", 0},
		{"", "0.0.1", -1},
	}
	for _, test := range tests {
		if got := compareVersions(test.a, test.b); got != test.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", test.a, test.b, got, test.want)
		}
	}
}
