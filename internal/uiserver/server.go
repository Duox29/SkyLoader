// Package uiserver serves plugin UI bundles from the local machine and bridges
// their JavaScript to the in-game plugin DLL.
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

func (s *Server) handlePlugin(w http.ResponseWriter, request *http.Request) {
	id, rest, ok := s.parsePluginPath(request.URL.Path)
	if !ok {
		http.NotFound(w, request)
		return
	}
	plugin, ok := s.source.PluginUI(id)
	if !ok {
		http.NotFound(w, request)
		return
	}

	root := filepath.Clean(plugin.Root)
	relative := entryPath(plugin.Entry)
	if rest != "" {
		relative = strings.TrimPrefix(path.Clean("/"+rest), "/")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if !inside(root, target) {
		http.NotFound(w, request)
		return
	}

	info, err := os.Stat(target)
	if err != nil {
		if err != nil && !strings.Contains(path.Base(relative), ".") {
			target = filepath.Join(root, filepath.FromSlash(entryPath(plugin.Entry)))
		} else {
			http.NotFound(w, request)
			return
		}
	} else if info.IsDir() {
		target = filepath.Join(target, filepath.FromSlash(entryPath(plugin.Entry)))
	}

	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")

	if strings.EqualFold(filepath.Ext(target), ".html") || strings.EqualFold(filepath.Ext(target), ".htm") {
		s.serveHTML(w, request, plugin, target)
		return
	}

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

func (s *Server) serveHTML(w http.ResponseWriter, request *http.Request, plugin Plugin, target string) {
	data, err := os.ReadFile(target)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	config := map[string]any{
		"plugin": map[string]string{
			"id":      plugin.ID,
			"name":    plugin.Name,
			"version": plugin.Version,
		},
		"rpcUrl":    fmt.Sprintf("/api/%s/%s/rpc", s.token, url.PathEscape(plugin.ID)),
		"eventsUrl": fmt.Sprintf("/api/%s/%s/events", s.token, url.PathEscape(plugin.ID)),
	}
	encoded, _ := json.Marshal(config)
	configJSON := strings.ReplaceAll(string(encoded), "</", "<\\/")

	base := `<base href="/p/` + s.token + `/` + url.PathEscape(plugin.ID) + `/">`
	injection := base + `<script>window.__SKY_UI__=` + configJSON + `;</script>` +
		`<script src="/_sky/bridge.js"></script>`

	html := string(data)
	lower := strings.ToLower(html)
	if index := strings.Index(lower, "<head"); index >= 0 {
		if end := strings.IndexByte(html[index:], '>'); end >= 0 {
			insert := index + end + 1
			html = html[:insert] + injection + html[insert:]
		} else {
			html = injection + html
		}
	} else {
		html = injection + html
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

func (s *Server) handleBridge(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(bridgeJS)
}

func (s *Server) handleAPI(w http.ResponseWriter, request *http.Request) {
	id, action, ok := s.parseAPIPath(request.URL.Path)
	if !ok {
		http.NotFound(w, request)
		return
	}
	switch action {
	case "rpc":
		s.handleRPC(w, request, id)
	case "events":
		s.handleEvents(w, request, id)
	default:
		http.NotFound(w, request)
	}
}

func (s *Server) handleRPC(w http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(request.Body, (2<<20)+1))
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	if len(payload) > 2<<20 {
		http.Error(w, "Payload Too Large", http.StatusRequestEntityTooLarge)
		return
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = []byte("null")
	}

	response, err := s.source.PluginRPC(id, payload)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	if len(bytes.TrimSpace(response)) == 0 {
		response = []byte("null")
	}
	if !json.Valid(response) {
		response, _ = json.Marshal(map[string]string{"raw": string(response)})
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}

func (s *Server) handleEvents(w http.ResponseWriter, request *http.Request, id string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": connected\n\n")
	flusher.Flush()

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
				if !json.Valid(event) {
					event, _ = json.Marshal(string(event))
				}
				_, _ = io.WriteString(w, "data: "+string(event)+"\n\n")
			}
			if len(events) > 0 {
				flusher.Flush()
			}
		}
	}
}

// parsePluginPath splits /p/<token>/<id>/<rest> and validates the token.
func (s *Server) parsePluginPath(requestPath string) (id, rest string, ok bool) {
	trimmed := strings.TrimPrefix(requestPath, "/p/")
	if trimmed == requestPath {
		return "", "", false
	}
	token, remainder, found := strings.Cut(trimmed, "/")
	if !found || token != s.token {
		return "", "", false
	}
	rawID, remainder, found := strings.Cut(remainder, "/")
	if !found || rawID == "" {
		return "", "", false
	}
	// request.URL.Path is already percent-decoded; unescaping it again would
	// corrupt legacy identities that contain a literal percent sign.
	return rawID, remainder, true
}

// parseAPIPath splits /api/<token>/<id>/<action> and validates the token.
func (s *Server) parseAPIPath(requestPath string) (id, action string, ok bool) {
	trimmed := strings.TrimPrefix(requestPath, "/api/")
	if trimmed == requestPath {
		return "", "", false
	}
	token, remainder, found := strings.Cut(trimmed, "/")
	if !found || token != s.token {
		return "", "", false
	}
	rawID, remainder, found := strings.Cut(remainder, "/")
	if !found || rawID == "" {
		return "", "", false
	}
	return rawID, strings.TrimSuffix(remainder, "/"), true
}

func entryPath(entry string) string {
	entry = strings.TrimSpace(strings.TrimPrefix(entry, "/"))
	if entry == "" {
		return "index.html"
	}
	return entry
}

func inside(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
