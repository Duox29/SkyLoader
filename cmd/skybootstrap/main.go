//go:build windows && cgo

// Command skybootstrap builds SkyBootstrap.dll, the in-game host injected into
// Sky.exe. It owns the named-pipe server, plugin DLL lifecycle, ABI callbacks,
// and per-plugin UI event queues.
//
// Layout: the startup lifecycle and the stable C entry points sit at the top;
// host internals and the pipe protocol fill the middle; low-level Win32/CGO
// helpers gather at the bottom of the file.
package main

/*
#include <stdint.h>
#include <wchar.h>
*/
import "C"

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// version is the user-visible SkyLoader release version. Keep it in sync
	// with launcher.Version; the DLL is a standalone c-shared command.
	version          = "0.2.0"
	pipeName         = `\\.\pipe\sky_bootstrap` // must match uiserver.PipeName
	apiVersion       = 4
	pipeBuffer       = 8 << 20
	uiMessageLimit   = 4 << 20
	maxEventCount    = 256
	maxEventBytes    = 1 << 20
	maxEventOneEvent = 64 << 10
)

var (
	startOnce       sync.Once
	globalHost      *host
	logCallbackPtr  uintptr
	postCallbackPtr uintptr
)

// --- startup lifecycle ------------------------------------------------------

// startHost initializes the host and launches the pipe server. It is safe to
// call repeatedly and is invoked automatically when the DLL is loaded.
func startHost() {
	startOnce.Do(func() {
		globalHost = newHost()
		logCallbackPtr = syscall.NewCallback(logTrampoline)
		postCallbackPtr = syscall.NewCallback(postEventTrampoline)
		globalHost.logger.Printf("SkyBootstrap %s starting in PID %d (ABI %d)",
			version, windows.GetCurrentProcessId(), apiVersion)
		go globalHost.pipeLoop()
	})
}

func init() {
	// c-shared libraries do not call main, so start the host during package
	// initialization. startHost only spawns the pipe loop; DllMain returns at
	// once.
	startHost()
}

func logTrampoline(message *byte) uintptr {
	if globalHost != nil && message != nil {
		globalHost.logger.Printf("%s", windows.BytePtrToString(message))
	}
	return 0
}

func postEventTrampoline(context uintptr, message *byte) uintptr {
	if globalHost == nil || message == nil {
		return 0
	}
	if globalHost.postEvent(context, windows.BytePtrToString(message)) {
		return 1
	}
	return 0
}

// The exported functions below are the stable C entry points used by native
// plugins and external tools. The named-pipe server is started when the DLL
// loads; these functions are a secondary interface. startHost guarantees a
// non-nil globalHost before each call.

//export SkyBootstrapStart
func SkyBootstrapStart() C.int {
	startHost()
	return 1
}

// SkyBootstrapLoadPluginW loads and initializes one plugin DLL.
//
//export SkyBootstrapLoadPluginW
func SkyBootstrapLoadPluginW(path *C.wchar_t) C.int {
	if path == nil {
		return 0
	}
	startHost()
	if err := globalHost.loadPlugin(utf16Arg(path)); err != nil {
		return 0
	}
	return 1
}

// SkyBootstrapUnloadPluginW calls SkyPluginShutdown, if present, and unloads
// the DLL.
//
//export SkyBootstrapUnloadPluginW
func SkyBootstrapUnloadPluginW(path *C.wchar_t) C.int {
	if path == nil {
		return 0
	}
	startHost()
	if err := globalHost.unloadPlugin(utf16Arg(path)); err != nil {
		return 0
	}
	return 1
}

// SkyBootstrapPluginCount returns the number of loaded plugins.
//
//export SkyBootstrapPluginCount
func SkyBootstrapPluginCount() C.uint32_t {
	startHost()
	globalHost.mu.RLock()
	defer globalHost.mu.RUnlock()
	return C.uint32_t(len(globalHost.plugins))
}

//export SkyBootstrapApiVersion
func SkyBootstrapApiVersion() C.uint32_t {
	return C.uint32_t(apiVersion)
}

func main() {}

// --- host types and lifecycle -------------------------------------------------

type host struct {
	logger *log.Logger
	file   *os.File

	loadMu      sync.Mutex // serializes LoadLibrary/FreeLibrary
	mu          sync.RWMutex
	plugins     map[string]*pluginHost // key: lower-case normalized path
	contexts    map[uintptr]*pluginHost
	nextContext uintptr
}

type pluginHost struct {
	path      string
	module    windows.Handle
	api       *pluginAPI
	init      uintptr
	shutdown  uintptr
	uiMessage uintptr
	context   uintptr

	callMu sync.Mutex // serializes calls into this plugin

	eventsMu   sync.Mutex
	events     []json.RawMessage
	eventBytes int
}

type pluginAPI struct {
	Version      uint32
	_            uint32
	Log          uintptr
	PostEvent    uintptr
	EventContext uintptr
}

func newHost() *host {
	file, logger := bootstrapLogger()
	return &host{
		logger:   logger,
		file:     file,
		plugins:  map[string]*pluginHost{},
		contexts: map[uintptr]*pluginHost{},
	}
}

// loadPlugin resolves the path, refuses duplicates, and loads/initializes the
// DLL under the load lock.
func (h *host) loadPlugin(rawPath string) error {
	path, key, err := cleanPluginPath(rawPath)
	if err != nil {
		return err
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return fmt.Errorf("plugin not found: %s", path)
	}

	h.loadMu.Lock()
	defer h.loadMu.Unlock()
	if h.loadedPlugin(key) != nil {
		return nil
	}
	return h.loadAndInitPlugin(path, key)
}

// loadAndInitPlugin loads the DLL, binds its exports, and calls SkyPluginInit.
// A rejected init unmaps the module again.
func (h *host) loadAndInitPlugin(path, key string) error {
	module, err := windows.LoadLibraryEx(path, 0,
		windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	if err != nil {
		// Windows versions without the search flags still support the plain
		// call. Retrying also preserves the previous behaviour on older hosts.
		module, err = windows.LoadLibrary(path)
	}
	if err != nil {
		return fmt.Errorf("LoadLibrary failed: %w", err)
	}

	initProc, err := windows.GetProcAddress(module, "SkyPluginInit")
	if err != nil || initProc == 0 {
		_ = windows.FreeLibrary(module)
		return errors.New("SkyPluginInit export is missing")
	}
	shutdownProc, _ := windows.GetProcAddress(module, "SkyPluginShutdown")
	uiProc, _ := windows.GetProcAddress(module, "SkyPluginUiMessage")

	context := h.allocateContext()
	// The API contains only integer callback addresses, so keeping it in Go
	// memory is safe. plugin.api keeps it reachable for as long as the plugin
	// may call into the host.
	api := &pluginAPI{
		Version:      apiVersion,
		Log:          logCallbackPtr,
		PostEvent:    postCallbackPtr,
		EventContext: context,
	}

	plugin := &pluginHost{
		path:      path,
		module:    module,
		api:       api,
		init:      initProc,
		shutdown:  shutdownProc,
		uiMessage: uiProc,
		context:   context,
		events:    make([]json.RawMessage, 0, 8),
	}
	h.mu.Lock()
	h.contexts[context] = plugin
	h.mu.Unlock()

	result, _, _ := syscall.SyscallN(initProc, uintptr(unsafe.Pointer(api)))
	runtime.KeepAlive(api)
	if int32(result) == 0 {
		h.mu.Lock()
		delete(h.contexts, context)
		h.mu.Unlock()
		_ = windows.FreeLibrary(module)
		return errors.New("SkyPluginInit rejected the bootstrap API")
	}

	h.mu.Lock()
	h.plugins[key] = plugin
	h.mu.Unlock()
	h.logger.Printf("Loaded plugin %s", path)
	return nil
}

// loadedPlugin reports the plugin registered under a lower-case path key.
func (h *host) loadedPlugin(key string) *pluginHost {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.plugins[key]
}

func (h *host) unloadPlugin(rawPath string) error {
	path, key, err := cleanPluginPath(rawPath)
	if err != nil {
		return err
	}

	h.loadMu.Lock()
	defer h.loadMu.Unlock()

	h.mu.Lock()
	plugin := h.plugins[key]
	if plugin != nil {
		delete(h.plugins, key)
	}
	h.mu.Unlock()
	if plugin == nil {
		return errors.New("plugin is not loaded")
	}

	plugin.callMu.Lock()
	if plugin.shutdown != 0 {
		syscall.SyscallN(plugin.shutdown)
	}
	plugin.callMu.Unlock()

	h.mu.Lock()
	delete(h.contexts, plugin.context)
	h.mu.Unlock()

	_ = windows.FreeLibrary(plugin.module)
	h.logger.Printf("Unloaded plugin %s", path)
	return nil
}

func (h *host) allocateContext() uintptr {
	h.mu.Lock()
	defer h.mu.Unlock()
	for {
		h.nextContext++
		if h.nextContext == 0 {
			continue
		}
		if _, exists := h.contexts[h.nextContext]; !exists {
			return h.nextContext
		}
	}
}

func (h *host) listPlugins() []string {
	h.mu.RLock()
	paths := make([]string, 0, len(h.plugins))
	for _, plugin := range h.plugins {
		paths = append(paths, plugin.path)
	}
	h.mu.RUnlock()
	sort.Slice(paths, func(i, j int) bool {
		return strings.ToLower(paths[i]) < strings.ToLower(paths[j])
	})
	return paths
}

// uiMessageFor forwards one UI message to a plugin's SkyPluginUiMessage
// export and returns its response buffer.
func (h *host) uiMessageFor(rawPath string, payload []byte) ([]byte, error) {
	_, key, err := cleanPluginPath(rawPath)
	if err != nil {
		return nil, err
	}

	// Hold the read lock until callMu is held. unloadPlugin needs the write
	// lock before it can free the DLL, so the module cannot disappear between
	// the lookup and the call.
	h.mu.RLock()
	plugin := h.plugins[key]
	if plugin == nil {
		h.mu.RUnlock()
		return nil, errors.New("plugin is not loaded")
	}
	if plugin.uiMessage == 0 {
		h.mu.RUnlock()
		return nil, errors.New("plugin does not export SkyPluginUiMessage")
	}
	plugin.callMu.Lock()
	h.mu.RUnlock()

	request := append(append([]byte(nil), payload...), 0)
	response := make([]byte, uiMessageLimit+1)
	result, _, _ := syscall.SyscallN(plugin.uiMessage,
		uintptr(unsafe.Pointer(&request[0])),
		uintptr(unsafe.Pointer(&response[0])),
		uintptr(uiMessageLimit))
	plugin.callMu.Unlock()
	runtime.KeepAlive(request)
	runtime.KeepAlive(response)

	length := int32(result)
	if length < 0 {
		return nil, errors.New("plugin UI handler reported an error")
	}
	if int(length) > uiMessageLimit {
		return nil, errors.New("plugin UI response is too large")
	}
	return response[:int(length)], nil
}

// postEvent queues one JSON event for the plugin owning context, enforcing
// the per-plugin event budget.
func (h *host) postEvent(context uintptr, message string) bool {
	if message == "" || len(message) > maxEventOneEvent {
		return false
	}
	raw := json.RawMessage(append([]byte(nil), message...))
	if !json.Valid(raw) {
		quoted, err := json.Marshal(message)
		if err != nil {
			return false
		}
		raw = json.RawMessage(quoted)
	}

	h.mu.RLock()
	plugin := h.contexts[context]
	h.mu.RUnlock()
	if plugin == nil {
		return false
	}

	plugin.eventsMu.Lock()
	defer plugin.eventsMu.Unlock()
	if len(plugin.events) >= maxEventCount || plugin.eventBytes+len(raw) > maxEventBytes {
		return false
	}
	plugin.events = append(plugin.events, raw)
	plugin.eventBytes += len(raw)
	return true
}

// pollEvents drains a plugin's queued events as a JSON array reply.
func (h *host) pollEvents(rawPath string) []byte {
	_, key, err := cleanPluginPath(rawPath)
	if err != nil {
		return []byte("[]")
	}

	h.mu.RLock()
	plugin := h.plugins[key]
	h.mu.RUnlock()
	if plugin == nil {
		return []byte("[]")
	}

	plugin.eventsMu.Lock()
	events := plugin.events
	plugin.events = make([]json.RawMessage, 0, 8)
	plugin.eventBytes = 0
	plugin.eventsMu.Unlock()
	if events == nil {
		events = []json.RawMessage{}
	}
	data, err := json.Marshal(events)
	if err != nil {
		return []byte("[]")
	}
	return data
}

// --- pipe server --------------------------------------------------------

func (h *host) pipeLoop() {
	name, err := windows.UTF16PtrFromString(pipeName)
	if err != nil {
		h.logger.Printf("invalid pipe name: %v", err)
		return
	}
	for {
		pipe, err := windows.CreateNamedPipe(name,
			windows.PIPE_ACCESS_DUPLEX,
			windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE|windows.PIPE_WAIT,
			windows.PIPE_UNLIMITED_INSTANCES,
			pipeBuffer, pipeBuffer, 0, nil)
		if err != nil {
			h.logger.Printf("CreateNamedPipe failed: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		err = windows.ConnectNamedPipe(pipe, nil)
		if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			_ = windows.CloseHandle(pipe)
			time.Sleep(25 * time.Millisecond)
			continue
		}
		go h.handleConnection(pipe)
	}
}

func (h *host) handleConnection(pipe windows.Handle) {
	defer windows.CloseHandle(pipe)
	defer windows.DisconnectNamedPipe(pipe)

	buffer := make([]byte, pipeBuffer)
	for {
		var read uint32
		err := windows.ReadFile(pipe, buffer, &read, nil)
		if err != nil {
			if !errors.Is(err, windows.ERROR_BROKEN_PIPE) &&
				!errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
				h.logger.Printf("pipe read: %v", err)
			}
			return
		}
		if read == 0 {
			return
		}
		command := strings.TrimRight(string(buffer[:read]), "\x00\r\n")
		response := h.handleCommand(command)
		var written uint32
		if err := windows.WriteFile(pipe, []byte(response), &written, nil); err != nil {
			h.logger.Printf("pipe write: %v", err)
			return
		}
		_ = windows.FlushFileBuffers(pipe)
	}
}

// handleCommand maps one pipe command line to its reply. Replies end with a
// newline; failures carry a structured "FAIL <code> <message>" body.
func (h *host) handleCommand(command string) string {
	switch {
	case command == "PING":
		return "OK\n"
	case command == "LIST":
		data, _ := json.Marshal(h.listPlugins())
		return "OK " + string(data) + "\n"
	case strings.HasPrefix(command, "LOAD "):
		return h.loadCommand(trimArgument(command, "LOAD "))
	case strings.HasPrefix(command, "UNLOAD "):
		return h.unloadCommand(trimArgument(command, "UNLOAD "))
	case strings.HasPrefix(command, "UI "):
		return h.uiCommand(strings.TrimPrefix(command, "UI "))
	case strings.HasPrefix(command, "POLL "):
		return h.pollCommand(trimArgument(command, "POLL "))
	default:
		return "FAIL UNKNOWN_COMMAND Unrecognized command\n"
	}
}

// trimArgument strips a command prefix from its path argument, as in
// "LOAD C:\...\mod.dll".
func trimArgument(command, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(command, prefix))
}

func (h *host) loadCommand(argument string) string {
	if argument == "" {
		return "FAIL LOAD_FAILED Empty plugin path\n"
	}
	if err := h.loadPlugin(argument); err != nil {
		return failReply("LOAD_FAILED", err)
	}
	return "OK\n"
}

func (h *host) unloadCommand(argument string) string {
	if argument == "" {
		return "FAIL UNLOAD_FAILED Empty plugin path\n"
	}
	if err := h.unloadPlugin(argument); err != nil {
		return failReply("UNLOAD_FAILED", err)
	}
	return "OK\n"
}

// uiCommand handles "UI <path> <base64 payload>": the payload is decoded and
// the base64 response is returned after "OK ".
func (h *host) uiCommand(argument string) string {
	index := strings.LastIndexByte(argument, ' ')
	if index <= 0 || index == len(argument)-1 {
		return "FAIL UI_FAILED Malformed UI command\n"
	}
	payload, err := base64.StdEncoding.DecodeString(argument[index+1:])
	if err != nil {
		return "FAIL UI_FAILED Invalid base64 payload\n"
	}
	if len(payload) == 0 {
		payload = []byte("null")
	}

	response, err := h.uiMessageFor(strings.TrimSpace(argument[:index]), payload)
	if err != nil {
		return failReply("UI_FAILED", err)
	}
	if len(response) == 0 {
		return "OK\n"
	}
	return "OK " + base64.StdEncoding.EncodeToString(response) + "\n"
}

func (h *host) pollCommand(argument string) string {
	if argument == "" {
		return "FAIL POLL_FAILED Empty plugin path\n"
	}
	return "OK " + string(h.pollEvents(argument)) + "\n"
}

// failReply renders a structured FAIL reply with a single-line message.
func failReply(code string, err error) string {
	return "FAIL " + code + " " + oneLine(err.Error()) + "\n"
}

// --- low-level helpers ------------------------------------------------------

// bootstrapLogger aims SkyBootstrap logging at SkyLoader's local log
// directory. A failure to touch one is not fatal: loading native plugins must
// not depend on logging, so logging silently disappears instead.
func bootstrapLogger() (*os.File, *log.Logger) {
	directory := logDirectory()
	if file, err := openLogFile(directory); err == nil {
		return file, log.New(file, "[SkyBootstrap] ", log.LstdFlags|log.Lmicroseconds)
	}
	return nil, log.New(io.Discard, "", 0)
}

// logDirectory prefers %LOCALAPPDATA%\SkyLoader\logs and falls back to a
// folder next to the DLL when that variable is absent.
func logDirectory() string {
	if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
		return filepath.Join(local, "SkyLoader", "logs")
	}
	if executable, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(executable), "SkyLoader", "logs")
	}
	return ""
}

func openLogFile(directory string) (*os.File, error) {
	if directory == "" {
		return nil, errors.New("no log directory")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, err
	}
	return os.OpenFile(filepath.Join(directory, "SkyBootstrap.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

// cleanPluginPath resolves and normalizes a plugin path, returning the
// canonical path and its lower-case registry key.
func cleanPluginPath(rawPath string) (path, key string, err error) {
	path, err = filepath.Abs(strings.TrimSpace(rawPath))
	if err != nil {
		return "", "", err
	}
	path = filepath.Clean(path)
	return path, strings.ToLower(path), nil
}

// utf16Arg converts a C wide-string argument into a Go string.
func utf16Arg(path *C.wchar_t) string {
	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(path)))
}

// oneLine flattens a message so pipe replies stay on one line.
func oneLine(message string) string {
	message = strings.ReplaceAll(message, "\r", " ")
	message = strings.ReplaceAll(message, "\n", " ")
	if len(message) > 400 {
		message = message[:400]
	}
	return strings.TrimSpace(message)
}
