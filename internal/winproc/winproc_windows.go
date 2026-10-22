//go:build windows

// Package winproc contains the small set of Windows process, registry, and
// injection operations SkyLoader needs.
package winproc

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

// Process identifies the running Sky process.
type Process struct {
	PID  uint32
	Path string
}

var (
	kernel32               = windows.NewLazySystemDLL("kernel32.dll")
	procCreateRemoteThread = kernel32.NewProc("CreateRemoteThread")
	procVirtualAllocEx     = kernel32.NewProc("VirtualAllocEx")
	procVirtualFreeEx      = kernel32.NewProc("VirtualFreeEx")
	procGetExitCodeThread  = kernel32.NewProc("GetExitCodeThread")
)

const skyRelativePath = `steamapps\common\Sky Children of the Light\Sky.exe`

// NormalizePath returns a clean absolute Windows path when possible.
func NormalizePath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "/", `\`))
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	return filepath.Clean(path)
}

// FileExists reports whether path is an existing regular file.
func FileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// FindProcess looks for exeName. When expectedPath is non-empty, a process
// whose image path matches it is preferred; otherwise the first match is
// returned.
func FindProcess(exeName, expectedPath string) (Process, bool) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return Process{}, false
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	var fallback Process
	expectedPath = NormalizePath(expectedPath)

	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		name := windows.UTF16ToString(entry.ExeFile[:])
		if !strings.EqualFold(name, exeName) {
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
	if fallback.PID != 0 {
		return fallback, true
	}
	return Process{}, false
}

func processImageName(process windows.Handle) string {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return ""
	}
	return NormalizePath(windows.UTF16ToString(buffer[:size]))
}

// InjectDLL loads dllPath into the target process with a remote LoadLibraryW
// call and waits for the load to finish.
func InjectDLL(pid uint32, dllPath string, timeout time.Duration) error {
	dllPath = NormalizePath(dllPath)
	if !FileExists(dllPath) {
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

	loadLibraryAddr := kernel32.NewProc("LoadLibraryW").Addr()

	var threadID uint32
	thread, _, callErr := procCreateRemoteThread.Call(
		uintptr(process), 0, 0, loadLibraryAddr, remote, 0, uintptr(unsafe.Pointer(&threadID)))
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

// OpenPath opens a file or folder with the user's default handler.
func OpenPath(path string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(path), nil, nil, windows.SW_SHOWNORMAL)
}

// OpenURL opens a URL with the default browser or protocol handler.
func OpenURL(url string) error {
	return windows.ShellExecute(0, windows.StringToUTF16Ptr("open"),
		windows.StringToUTF16Ptr(url), nil, nil, windows.SW_SHOWNORMAL)
}

// DetectSkyPath searches Steam's registry entries, all library folders, and
// common drive layouts for Sky.exe.
func DetectSkyPath() string {
	if path := detectFromSteam(); path != "" {
		return path
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
			if FileExists(candidate) {
				return NormalizePath(candidate)
			}
		}
	}
	return ""
}

func detectFromSteam() string {
	steamPath := steamPathFromRegistry()
	if steamPath == "" {
		return ""
	}
	for _, library := range steamLibraries(steamPath) {
		candidate := filepath.Join(library, skyRelativePath)
		if FileExists(candidate) {
			return NormalizePath(candidate)
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
	locations := []location{
		{registry.CURRENT_USER, `Software\Valve\Steam`, "SteamPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\WOW6432Node\Valve\Steam`, "InstallPath"},
		{registry.LOCAL_MACHINE, `SOFTWARE\Valve\Steam`, "InstallPath"},
	}
	for _, location := range locations {
		key, err := registry.OpenKey(location.root, location.path, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		value, _, err := key.GetStringValue(location.value)
		key.Close()
		if err == nil && value != "" {
			return NormalizePath(value)
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
		value := unescapeVDF(string(match[1]))
		if value == "" {
			continue
		}
		value = NormalizePath(value)
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
			builder.WriteByte(value[i])
			continue
		}
		builder.WriteByte(value[i])
	}
	return builder.String()
}
