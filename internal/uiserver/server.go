// Package uiserver serves plugin UI bundles from the local machine, bridges
// their JavaScript to the in-game plugin DLL, and implements the named-pipe
// contract with SkyBootstrap.
//
// server.go keeps each route handler short: request decoding, path mapping,
// and response writing live in the helper section at the bottom of the file.
package uiserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// maxRPCPayload bounds one plugin UI request body.
const maxRPCPayload = 2 << 20

// Plugin is the UI-facing subset of a registered plugin.
type Plugin struct {
	ID      string
	Name    string
	Version string
	Root    string
	Entry   string
}

// Source connects the HTTP layer to the launcher. Implementations must be safe
// for concurrent use.
type Source interface {
	PluginUI(id string) (Plugin, bool)
	PluginRPC(id string, payload []byte) ([]byte, error)
	PluginEvents(id string) ([][]byte, error)
}

// Server serves plugin bundles on a random loopback port. The random token is
// part of every URL path so that relative assets inherit authorization without
// relying on third-party cookies inside the launcher's WebView.
type Server struct {
	source   Source
	token    string
	listener net.Listener
	http     *http.Server
	baseURL  string
}

// New starts the local UI server.
func New(source Source) (*Server, error) {
	tokenBytes := make([]byte, 24)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	server := &Server{
		source: source,
		token:  hex.EncodeToString(tokenBytes),
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	server.listener = listener
	server.baseURL = "http://" + listener.Addr().String()

	mux := http.NewServeMux()
	mux.HandleFunc("/p/", server.handlePlugin)
	mux.HandleFunc("/_sky/bridge.js", server.handleBridge)
	mux.HandleFunc("/api/", server.handleAPI)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	server.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		_ = server.http.Serve(listener)
	}()
	return server, nil
}

// BaseURL returns the loopback origin without a token.
func (s *Server) BaseURL() string { return s.baseURL }

// PluginURL returns the authenticated entry URL for a plugin bundle.
func (s *Server) PluginURL(id string) string {
	return fmt.Sprintf("%s/p/%s/%s/", s.baseURL, s.token, url.PathEscape(id))
}

// Close stops the HTTP server.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.http.Shutdown(ctx)
}

// --- routes -------------------------------------------------------------

func (s *Server) handlePlugin(w http.ResponseWriter, request *http.Request) {
	id, rest, ok := s.segment(request.URL.Path, "/p/")
	if !ok {
		http.NotFound(w, request)
		return
	}
	plugin, ok := s.source.PluginUI(id)
	if !ok {
		http.NotFound(w, request)
		return
	}

	target, ok := resolveBundleAsset(plugin, rest)
	if !ok {
		http.NotFound(w, request)
		return
	}

	noStoreHeaders(w)
	if isHTMLFile(target) {
		s.serveHTML(w, request, plugin, target)
		return
	}
	s.serveAsset(w, request, target)
}

// serveHTML delivers an entry page with the bridge injected, so the page boots
// inside the launcher with its plugin environment ready.
func (s *Server) serveHTML(w http.ResponseWriter, request *http.Request, plugin Plugin, target string) {
	data, err := os.ReadFile(target)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(injectBridgeHTML(string(data), plugin, s.token)))
}

func (s *Server) handleBridge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(bridgeJS)
}

func (s *Server) handleAPI(w http.ResponseWriter, request *http.Request) {
	id, rest, ok := s.segment(request.URL.Path, "/api/")
	if !ok {
		http.NotFound(w, request)
		return
	}
	switch strings.TrimSuffix(rest, "/") {
	case "rpc":
		s.handleRPC(w, request, id)
	case "events":
		s.handleEvents(w, request, id)
	default:
		http.NotFound(w, request)
	}
}

// handleRPC carries one plugin UI message to its DLL. Plugin failures keep
// HTTP 502 semantics while the JSON document carries the message.
func (s *Server) handleRPC(w http.ResponseWriter, request *http.Request, id string) {
	payload, ok := readPayload(w, request, maxRPCPayload)
	if !ok {
		return
	}

	response, err := s.source.PluginRPC(id, payload)
	if err != nil {
		respondRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ensureJSONDocument(response))
}

// handleEvents streams queued plugin events as server-sent events until the
// client disconnects.
func (s *Server) handleEvents(w http.ResponseWriter, request *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	openEventStream(w, flusher)
	s.pumpEvents(w, request, flusher, id)
}

// --- helpers: paths and serving ---------------------------------------------

// resolveBundleAsset maps a bundle-relative request path onto the file to
// serve: directories and route-less misses resolve to the bundle's entry page,
// real assets are served as-is, and anything escaping the bundle root is
// rejected.
func resolveBundleAsset(plugin Plugin, rest string) (string, bool) {
	root := filepath.Clean(plugin.Root)
	relative := entryPath(plugin.Entry)
	if rest != "" {
		relative = strings.TrimPrefix(path.Clean("/"+rest), "/")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	// Relative paths may not escape the bundle root.
	if relative, err := filepath.Rel(root, target); err != nil ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}

	info, statErr := os.Stat(target)
	switch {
	case statErr == nil && info.IsDir():
		target = filepath.Join(target, filepath.FromSlash(entryPath(plugin.Entry)))
	case statErr == nil:
		// An existing asset is served as-is.
	case statErr != nil && !strings.Contains(path.Base(relative), "."):
		// Route-less requests fall back to the bundle's entry page.
		target = filepath.Join(root, filepath.FromSlash(entryPath(plugin.Entry)))
	default:
		return "", false
	}
	return target, true
}

// serveAsset streams one plugin file, guessing the content type from its
// extension.
func (s *Server) serveAsset(w http.ResponseWriter, request *http.Request, target string) {
	file, err := os.Open(target)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	defer file.Close()
	if contentType := mime.TypeByExtension(filepath.Ext(target)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, request, filepath.Base(target), time.Time{}, file)
}

// isHTMLFile reports whether target is an entry document that needs the
// bridge injected.
func isHTMLFile(target string) bool {
	return strings.EqualFold(filepath.Ext(target), ".html") || strings.EqualFold(filepath.Ext(target), ".htm")
}

// noStoreHeaders marks bundle responses as uncacheable and un-sniffable.
func noStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
}

// injectBridgeHTML rewrites an entry document so it boots inside the
// launcher: a <base> tag pins relative assets to the authenticated prefix and
// the bridge and plugin environment are seeded head-first.
func injectBridgeHTML(html string, plugin Plugin, token string) string {
	injection := `<base href="/p/` + token + `/` + url.PathEscape(plugin.ID) + `/">` +
		`<script>window.__SKY_UI__=` + uiConfigJSON(plugin, token) + `;</script>` +
		`<script src="/_sky/bridge.js"></script>`

	lower := strings.ToLower(html)
	if index := strings.Index(lower, "<head"); index >= 0 {
		if end := strings.IndexByte(html[index:], '>'); end >= 0 {
			insert := index + end + 1
			return html[:insert] + injection + html[insert:]
		}
	}
	return injection + html
}

// uiConfigJSON renders the window.__SKY_UI__ environment document. "</" is
// escaped so plugins cannot close the surrounding <script> element.
func uiConfigJSON(plugin Plugin, token string) string {
	config := map[string]any{
		"plugin": map[string]string{
			"id":      plugin.ID,
			"name":    plugin.Name,
			"version": plugin.Version,
		},
		"rpcUrl":    fmt.Sprintf("/api/%s/%s/rpc", token, url.PathEscape(plugin.ID)),
		"eventsUrl": fmt.Sprintf("/api/%s/%s/events", token, url.PathEscape(plugin.ID)),
	}
	encoded, _ := json.Marshal(config)
	return strings.ReplaceAll(string(encoded), "</", "<\\/")
}

// --- helpers: server-sent events --------------------------------------------

// openEventStream writes the SSE response head so browsers begin streaming.
func openEventStream(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()
}

// pumpEvents drains a plugin's event queue on a short interval, keeping the
// stream alive with pings.
func (s *Server) pumpEvents(w http.ResponseWriter, request *http.Request, flusher http.Flusher, id string) {
	ticker := time.NewTicker(350 * time.Millisecond)
	defer ticker.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-request.Context().Done():
			return
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			flusher.Flush()
		case <-ticker.C:
			events, err := s.source.PluginEvents(id)
			if err != nil {
				continue
			}
			for _, event := range events {
				deliverEvent(w, event)
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

// deliverEvent writes one plugin event as an SSE data frame, wrapping plain
// strings so every frame stays valid JSON.
func deliverEvent(w http.ResponseWriter, raw []byte) {
	if !json.Valid(raw) {
		wrapped, _ := json.Marshal(string(raw))
		raw = wrapped
	}
	_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
}

// --- helpers: requests and responses -----------------------------------------

// readPayload reads a bounded POST body, substituting a JSON "null" when the
// client sent nothing. It writes the error response and reports failure, so
// callers can return immediately.
func readPayload(w http.ResponseWriter, request *http.Request, limit int64) ([]byte, bool) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, limit+1))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return nil, false
	}
	if int64(len(payload)) > limit {
		http.Error(w, "Payload Too Large", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = []byte("null")
	}
	return payload, true
}

// ensureJSONDocument maps a plugin response onto a JSON value: an empty
// payload becomes null and plain text is wrapped in {"raw": ...}.
func ensureJSONDocument(response []byte) []byte {
	if len(bytes.TrimSpace(response)) == 0 {
		return []byte("null")
	}
	if !json.Valid(response) {
		response, _ = json.Marshal(map[string]string{"raw": string(response)})
	}
	return response
}

// respondRPCError reports a plugin failure as a JSON error document.
func respondRPCError(w http.ResponseWriter, err error) {
	// json.NewEncoder responses end with a newline; keep that wire format.
	message, _ := json.Marshal(map[string]string{"error": err.Error()})
	writeJSON(w, http.StatusBadGateway, append(message, '\n'))
}

// segment splits /<prefix>/<token>/<id>/<rest> and validates the token.
func (s *Server) segment(requestPath, prefix string) (id, rest string, ok bool) {
	trimmed := strings.TrimPrefix(requestPath, prefix)
	if trimmed == requestPath {
		return "", "", false
	}
	token, remainder, found := strings.Cut(trimmed, "/")
	if !found || token != s.token {
		return "", "", false
	}
	id, rest, found = strings.Cut(remainder, "/")
	if !found || id == "" {
		return "", "", false
	}
	// request.URL.Path is already percent-decoded; unescaping it again would
	// corrupt legacy identities that contain a literal percent sign.
	return id, rest, true
}

func entryPath(entry string) string {
	entry = strings.TrimSpace(strings.TrimPrefix(entry, "/"))
	if entry == "" {
		return "index.html"
	}
	return entry
}

// writeJSON sends a JSON body with the shared no-cache headers.
func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
