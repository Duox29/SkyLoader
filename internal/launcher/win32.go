//go:build windows

package launcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	watchInterval   = 700 * time.Millisecond
	injectionLimit  = 30 * time.Second
	skyRelativePath = `steamapps\common\Sky Children of the Light\Sky.exe`
)

// Process identifies the running Sky process.
type Process struct {
	PID  uint32
	Path string
}

// Info is the normalized plugin manifest from a DLL's VERSIONINFO resource.
type Info struct {
	ID          string
	Version     string
	Name        string
	Author      string
	Description string
}

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procCreateRemoteThread = kernel32.NewProc("CreateRemoteThread")
	procVirtualAllocEx     = kernel32.NewProc("VirtualAllocEx")
	procVirtualFreeEx      = kernel32.NewProc("VirtualFreeEx")
	procGetExitCodeThread  = kernel32.NewProc("GetExitCodeThread")
	procLoadLibraryW       = kernel32.NewProc("LoadLibraryW")
)

// normalizePath returns a clean absolute Windows path when possible.
func normalizePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "/", `\`))
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

// findProcess looks for exeName. When expectedPath is non-empty, a process
// whose image path matches it is preferred; otherwise the first match is
// returned.
func findProcess(exeName, expectedPath string) (Process, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return Process{}, false
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	var fallback Process
	expectedPath = normalizePath(expectedPath)

	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if !strings.EqualFold(windows.UTF16ToString(entry.ExeFile[:]), exeName) {
			continue
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if err != nil {
			continue
		}
		path := processImageName(process)
		windows.CloseHandle(process)
		if path == "" {
			continue
		}
		candidate := Process{PID: entry.ProcessID, Path: path}
		if expectedPath == "" || strings.EqualFold(path, expectedPath) {
			return candidate, true
		}
		if fallback.PID == 0 {
			fallback = candidate
		}
	}
	return fallback, fallback.PID != 0
}

func processImageName(process windows.Handle) string {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return normalizePath(windows.UTF16ToString(buffer[:size]))
}

type versionTable map[string]string

func readVersionInfo(path string) (Info, error) {
	size, err := windows.GetFileVersionInfoSize(path, nil)
	if err != nil {
		return Info{}, err
	}
	if size == 0 {
		return Info{}, errors.New("no VERSIONINFO resource")
	}
	data := make([]byte, size)
	if err := windows.GetFileVersionInfo(path, 0, size, unsafe.Pointer(&data[0])); err != nil {
		return Info{}, err
	}
	block := unsafe.Pointer(&data[0])

	var tables []versionTable
	var translations unsafe.Pointer
	var length uint32
	if windows.VerQueryValue(block, `\VarFileInfo\Translation`, unsafe.Pointer(&translations), &length) == nil &&
		translations != nil && length >= 4 {
		pairs := unsafe.Slice((*uint16)(translations), length/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			tables = append(tables, readStringTable(block, pairs[i], pairs[i+1]))
		}
	}
	if len(tables) == 0 {
		// A few hand-built resources omit VarFileInfo; probe the usual
		// Unicode code pages instead of rejecting otherwise valid metadata.
		for _, pair := range [][2]uint16{{0x0409, 0x04B0}, {0x0000, 0x04B0}, {0x0409, 0x04E4}} {
			tables = append(tables, readStringTable(block, pair[0], pair[1]))
		}
	}
	return infoFromTables(tables)
}

func readStringTable(block unsafe.Pointer, language, codePage uint16) versionTable {
	prefix := fmt.Sprintf(`\StringFileInfo\%04x%04x\`, language, codePage)
	table := versionTable{}
	for _, key := range []string{
		"SkyPluginId", "Version", "ProductVersion", "FileVersion",
		"PluginName", "ProductName", "Author", "CompanyName",
		"Description", "FileDescription",
	} {
		table[key] = versionString(block, prefix+key)
	}
	return table
}

func versionString(block unsafe.Pointer, path string) string {
	var buffer *uint16
	var length uint32
	if windows.VerQueryValue(block, path, unsafe.Pointer(&buffer), &length) != nil ||
		buffer == nil || length == 0 {
		return ""
	}
	return windows.UTF16PtrToString(buffer)
}

func infoFromTables(tables []versionTable) (Info, error) {
	for _, table := range tables {
		id := Canonical(table["SkyPluginId"])
		if id == "" {
			continue
		}
		return Info{
			ID:          id,
			Version:     firstNonEmpty(table["Version"], table["ProductVersion"], table["FileVersion"]),
			Name:        firstNonEmpty(table["PluginName"], table["ProductName"]),
			Author:      firstNonEmpty(table["Author"], table["CompanyName"]),
			Description: firstNonEmpty(table["Description"], table["FileDescription"]),
		}, nil
	}
	return Info{}, errors.New("no usable SkyPluginId in VERSIONINFO")
}

// injectDLL loads dllPath into the target process with a remote LoadLibraryW
// call and waits for the load to finish.
func injectDLL(pid uint32, dllPath string, timeout time.Duration) error {
	dllPath = normalizePath(dllPath)
	if !isRegularFile(dllPath) {
		return errors.New("SkyBootstrap.dll was not found")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_CREATE_THREAD|windows.PROCESS_QUERY_INFORMATION|
			windows.PROCESS_VM_OPERATION|windows.PROCESS_VM_WRITE|windows.PROCESS_VM_READ,
		false, pid)
	if err != nil {
		return fmt.Errorf("OpenProcess failed: %w", err)
	}
	defer windows.CloseHandle(process)

	widePath, err := windows.UTF16FromString(dllPath)
	if err != nil {
		return err
	}
	size := uintptr(len(widePath) * 2)
	remote, _, callErr := procVirtualAllocEx.Call(
		uintptr(process), 0, size, windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_READWRITE)
	if remote == 0 {
		return fmt.Errorf("VirtualAllocEx failed: %v", callErr)
	}
	defer procVirtualFreeEx.Call(uintptr(process), remote, 0, windows.MEM_RELEASE)

	var written uintptr
	if err := windows.WriteProcessMemory(process, remote, (*byte)(unsafe.Pointer(&widePath[0])), size, &written); err != nil {
		return fmt.Errorf("WriteProcessMemory failed: %w", err)
	}
	if written != size {
		return errors.New("WriteProcessMemory wrote a partial path")
	}

	var threadID uint32
	thread, _, callErr := procCreateRemoteThread.Call(
		uintptr(process), 0, 0, procLoadLibraryW.Addr(), remote, 0, uintptr(unsafe.Pointer(&threadID)))
	if thread == 0 {
		return fmt.Errorf("CreateRemoteThread failed: %v", callErr)
	}
	defer windows.CloseHandle(windows.Handle(thread))

	event, err := windows.WaitForSingleObject(windows.Handle(thread), uint32(timeout.Milliseconds()))
	if err != nil {
		return fmt.Errorf("WaitForSingleObject failed: %w", err)
	}
	if event != windows.WAIT_OBJECT_0 {
		return errors.New("Sky.exe did not finish loading the DLL in time")
	}
	var exitCode uint32
	procGetExitCodeThread.Call(thread, uintptr(unsafe.Pointer(&exitCode)))
	runtime.KeepAlive(widePath)
	if exitCode == 0 {
		return errors.New("Sky.exe did not load the DLL")
	}
	return nil
}

// openPath opens a file or folder with the user's default handler.
func openPath(path string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(path), nil, nil, windows.SW_SHOWNORMAL)
}

// openURL opens a URL with the default browser or protocol handler.
func openURL(url string) error {
	return openPath(url)
}

// detectSkyPath searches Steam's registry entries, all library folders, and
// common drive layouts for Sky.exe.
func detectSkyPath() string {
	if steamPath := steamPathFromRegistry(); steamPath != "" {
		for _, library := range steamLibraries(steamPath) {
			candidate := filepath.Join(library, skyRelativePath)
			if isRegularFile(candidate) {
				return normalizePath(candidate)
			}
		}
	}
	for drive := 'A'; drive <= 'Z'; drive++ {
		root := string(drive) + `:\`
		for _, prefix := range []string{
			"",
			"SteamLibrary",
			"Steam",
			`Program Files (x86)\Steam`,
			`Program Files\Steam`,
			`Games\SteamLibrary`,
			`Games\Steam`,
			"Games",
		} {
			candidate := filepath.Join(root, prefix, skyRelativePath)
			if isRegularFile(candidate) {
				return normalizePath(candidate)
			}
		}
	}
	return ""
}

func steamPathFromRegistry() string {
	type location struct {
		root  registry.Key
		path  string
		value string
	}
	for _, location := range []location{
		{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"},
	} {
		key, err := registry.OpenKey(location.root, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetStringValue(location.value)
		key.Close()
		if err == nil && value != "" {
			return normalizePath(value)
		}
	}
	return ""
}

var libraryPathPattern = regexp.MustCompile(`(?m)"path"\s+"((?:[^"\\]|\\.)*)"`)

func steamLibraries(steamPath string) []string {
	libraries := []string{steamPath}
	data, err := os.ReadFile(filepath.Join(steamPath, "steamapps", "libraryfolders.vdf"))
	if err != nil {
		return libraries
	}
	for _, match := range libraryPathPattern.FindAllSubmatch(data, -1) {
		value := normalizePath(unescapeVDF(string(match[1])))
		if value == "" {
			continue
		}
		duplicate := false
		for _, existing := range libraries {
			if strings.EqualFold(existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			libraries = append(libraries, value)
		}
	}
	return libraries
}

func unescapeVDF(value string) string {
	var builder strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] == '\\' && i+1 < len(value) {
			i++
		}
		builder.WriteByte(value[i])
	}
	return builder.String()
}

func (l *Launcher) watch() {
	defer close(l.done)
	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	l.pollProcess()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			l.pollProcess()
		}
	}
}

func (l *Launcher) pollProcess() {
	l.mu.RLock()
	gamePath := l.cfg.GamePath
	l.mu.RUnlock()

	process, running := findProcess("Sky.exe", gamePath)

	l.mu.Lock()
	changed := running != l.gameRunning
	if running && l.gameRunning && process.PID != l.running.PID {
		changed = true
	}
	l.gameRunning = running
	if running {
		l.running = process
	} else if l.gameRunning || l.running.PID != 0 {
		l.running = Process{}
		l.injectedPID = 0
		l.attemptedPID = 0
		l.loaded = map[string]bool{}
		l.pending = false
	}

	autoInject := l.cfg.AutoInject
	pending := l.pending
	injectedPID := l.injectedPID
	attemptedPID := l.attemptedPID
	injectingPID := l.injectingPID
	var pathUpdate string
	if running && process.Path != "" && !strings.EqualFold(normalizePath(process.Path), normalizePath(gamePath)) {
		l.cfg.GamePath = normalizePath(process.Path)
		pathUpdate = l.cfg.GamePath
	}
	l.mu.Unlock()

	if pathUpdate != "" {
		if err := l.persist(); err != nil {
			l.logger.Printf("save detected Sky path: %v", err)
		}
	}

	if changed {
		if running {
			l.setStatus(fmt.Sprintf("Sky.exe detected (PID %d).", process.PID))
		} else {
			l.setStatus("Sky.exe exited; plugins are no longer loaded.")
		}
	}

	if !running {
		return
	}

	if (autoInject || pending) && injectedPID != process.PID &&
		attemptedPID != process.PID && injectingPID != process.PID {
		l.injectAsync(process)
	}
}

func (l *Launcher) injectAsync(process Process) {
	l.mu.Lock()
	if l.injectingPID == process.PID {
		l.mu.Unlock()
		return
	}
	l.injectingPID = process.PID
	l.attemptedPID = process.PID
	l.pending = false
	l.mu.Unlock()

	go l.inject(process)
}

func (l *Launcher) inject(process Process) {
	l.injectMu.Lock()
	defer l.injectMu.Unlock()

	l.mu.RLock()
	bootstrap := l.cfg.BootstrapPath
	enabled := make([]pluginRecord, 0, len(l.plugins))
	for _, key := range l.order {
		record := l.plugins[key]
		if record != nil && record.Enabled && !record.Missing {
			enabled = append(enabled, *record)
		}
	}
	l.mu.RUnlock()

	fail := func(message string) {
		l.mu.Lock()
		l.injectingPID = 0
		l.mu.Unlock()
		l.setStatus(message)
		l.Notice("error", message)
	}

	if bootstrap == "" || !isRegularFile(bootstrap) {
		fail("SkyBootstrap.dll was not found. Choose a valid bootstrap DLL in Settings.")
		return
	}
	if err := injectDLL(process.PID, bootstrap, injectionLimit); err != nil {
		fail("Could not inject SkyBootstrap into Sky.exe: " + err.Error())
		return
	}

	// The Go runtime starts the pipe server as the DLL loads. Ping with the
	// normal timeout so a slow initialization is not mistaken for failure.
	if err := l.client.Ping(); err != nil {
		fail("SkyBootstrap was injected, but its pipe did not respond: " + err.Error())
		return
	}

	loadedPaths := make([]string, 0, len(enabled))
	var failures []string
	for _, record := range enabled {
		if err := l.client.Load(record.DLLPath); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%v)", firstNonEmpty(record.Name, record.ID), err))
			l.logger.Printf("load plugin %s: %v", record.DLLPath, err)
			continue
		}
		loadedPaths = append(loadedPaths, record.DLLPath)
	}

	if authoritative, err := l.client.List(); err == nil {
		loadedPaths = authoritative
	}

	l.mu.Lock()
	stillRunning := l.gameRunning && l.running.PID == process.PID
	if stillRunning {
		l.injectedPID = process.PID
		l.injectingPID = 0
	}
	l.mu.Unlock()
	if !stillRunning {
		l.Notice("warning", "Sky.exe exited before SkyBootstrap finished loading.")
		return
	}

	l.setLoaded(loadedPaths)
	message := fmt.Sprintf("SkyBootstrap injected (PID %d); %d plugin(s) loaded.", process.PID, len(loadedPaths))
	if len(failures) > 0 {
		message = fmt.Sprintf("SkyBootstrap injected (PID %d), but %d plugin(s) failed to load: %s",
			process.PID, len(failures), strings.Join(failures, ", "))
		l.setStatus(message)
		l.Notice("warning", message)
		return
	}
	l.setStatus(message)
	l.Notice("success", message)
}
