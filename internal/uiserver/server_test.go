package uiserver

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeSource struct {
	root string
	rpc  []byte
}

func (f *fakeSource) PluginUI(id string) (Plugin, bool) {
	if id != "demo.plugin" {
		return Plugin{}, false
	}
	return Plugin{ID: id, Name: "Demo", Version: "1.0.0", Root: f.root, Entry: "index.html"}, true
}

func (f *fakeSource) PluginRPC(_ string, payload []byte) ([]byte, error) {
	response := map[string]any{"ok": true, "echo": json.RawMessage(payload)}
	return json.Marshal(response)
}

func (f *fakeSource) PluginEvents(string) ([][]byte, error) { return nil, nil }

func newTestServer(t *testing.T) (*Server, *fakeSource) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(`<!doctype html><html><head><title>Demo</title></head><body><script src="app.js"></script></body></html>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js"), []byte("console.log('ok')"), 0o644); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{root: root}
	server, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, source
}

func TestPluginPageAndAssets(t *testing.T) {
	server, _ := newTestServer(t)
	pluginURL := server.PluginURL("demo.plugin")

	response, err := http.Get(pluginURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d", response.StatusCode)
	}
	html := string(body)
	if !strings.Contains(html, "window.__SKY_UI__") || !strings.Contains(html, "/_sky/bridge.js") {
		t.Fatalf("bridge was not injected: %s", html)
	}
	if !strings.Contains(html, "/p/") || !strings.Contains(html, "/api/") {
		t.Fatalf("token paths were not injected: %s", html)
	}

	base := strings.TrimSuffix(pluginURL, "/") + "/app.js"
	asset, err := http.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	assetBody, _ := io.ReadAll(asset.Body)
	asset.Body.Close()
	if asset.StatusCode != http.StatusOK || string(assetBody) != "console.log('ok')" {
		t.Fatalf("asset = %d %q", asset.StatusCode, assetBody)
	}
}

func TestParsePluginPathKeepsDecodedPercent(t *testing.T) {
	server := &Server{token: "token"}
	id, rest, ok := server.segment("/p/token/100%.dll/index.html", "/p/")
	if !ok || id != "100%.dll" || rest != "index.html" {
		t.Fatalf("segment = %q, %q, %v", id, rest, ok)
	}
}

func TestRPCAndTraversal(t *testing.T) {
	server, _ := newTestServer(t)
	tokenPath := strings.Split(strings.TrimPrefix(server.PluginURL("demo.plugin"), server.BaseURL()+"/p/"), "/")[0]
	rpcURL := server.BaseURL() + "/api/" + tokenPath + "/demo.plugin/rpc"
	response, err := http.Post(rpcURL, "application/json", strings.NewReader(`{"ping":1}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !json.Valid(body) {
		t.Fatalf("rpc = %d %s", response.StatusCode, body)
	}

	badToken := strings.Replace(server.PluginURL("demo.plugin"), tokenPath, "nope", 1)
	bad, err := http.Get(badToken)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusForbidden && bad.StatusCode != http.StatusNotFound {
		t.Fatalf("bad token status = %d", bad.StatusCode)
	}

	traversal := strings.TrimSuffix(server.PluginURL("demo.plugin"), "/") + "/../../config.json"
	trav, err := http.Get(traversal)
	if err != nil {
		t.Fatal(err)
	}
	trav.Body.Close()
	if trav.StatusCode != http.StatusNotFound {
		t.Fatalf("traversal status = %d", trav.StatusCode)
	}
}
