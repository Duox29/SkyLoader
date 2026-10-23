//go:build windows

package uiserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PipeName is the fixed endpoint exposed by SkyBootstrap.
const PipeName = `\\.\pipe\sky_bootstrap`

// DefaultTimeout covers injection startup and plugin initialization.
const DefaultTimeout = 20 * time.Second

// RemoteError is a structured FAIL response from SkyBootstrap.
type RemoteError struct {
	Code    string
	Message string
}

func (e *RemoteError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

func LoadCommand(path string) string   { return "LOAD " + path }
func UnloadCommand(path string) string { return "UNLOAD " + path }

func UICommand(path string, payload []byte) string {
	return "UI " + path + " " + base64.StdEncoding.EncodeToString(payload)
}

func PollCommand(path string) string { return "POLL " + path }

func parseResponse(data []byte) ([]byte, error) {
	text := strings.TrimRight(string(data), "\r\n")
	switch {
	case text == "OK":
		return nil, nil
	case strings.HasPrefix(text, "OK "):
		return []byte(text[3:]), nil
	case strings.HasPrefix(text, "FAIL "):
		rest := text[5:]
		code, message, _ := strings.Cut(rest, " ")
		return nil, &RemoteError{Code: code, Message: message}
	case text == "":
		return nil, errors.New("SkyBootstrap returned an empty response")
	default:
		return nil, fmt.Errorf("unexpected SkyBootstrap response: %q", text)
	}
}

// DecodeUIResponse accepts a base64 payload from a UI command.
func DecodeUIResponse(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, fmt.Errorf("decode UI response: %w", err)
	}
	return decoded, nil
}

// DecodePollResponse parses the JSON array returned by POLL.
func DecodePollResponse(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var messages []json.RawMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("decode event list: %w", err)
	}
	out := make([][]byte, 0, len(messages))
	for _, message := range messages {
		out = append(out, []byte(message))
	}
	return out, nil
}

// DecodeListResponse parses the JSON array of loaded DLL paths.
func DecodeListResponse(data []byte) ([]string, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal(data, &paths); err != nil {
		return nil, fmt.Errorf("decode plugin list: %w", err)
	}
	return paths, nil
}

// Client talks to one SkyBootstrap pipe server. Each command opens a fresh
// connection so concurrent UI calls never share a message stream.
type Client struct {
	PipeName string
	Timeout  time.Duration
}

// NewClient returns a client with the default timeout.
func NewClient() *Client {
	return &Client{PipeName: PipeName, Timeout: DefaultTimeout}
}

var (
	kernel32       = windows.NewLazySystemDLL("kernel32.dll")
	procWaitNamed  = kernel32.NewProc("WaitNamedPipeW")
	procCancelIOEx = kernel32.NewProc("CancelIoEx")
)

func (c *Client) Load(path string) error {
	_, err := c.do(LoadCommand(path))
	return err
}

func (c *Client) Unload(path string) error {
	_, err := c.do(UnloadCommand(path))
	return err
}

func (c *Client) Ping() error {
	_, err := c.do("PING")
	return err
}

func (c *Client) UI(path string, payload []byte) ([]byte, error) {
	response, err := c.do(UICommand(path, payload))
	if err != nil {
		return nil, err
	}
	return DecodeUIResponse(response)
}

func (c *Client) Poll(path string) ([][]byte, error) {
	response, err := c.do(PollCommand(path))
	if err != nil {
		return nil, err
	}
	return DecodePollResponse(response)
}

func (c *Client) List() ([]string, error) {
	response, err := c.do("LIST")
	if err != nil {
		return nil, err
	}
	return DecodeListResponse(response)
}

func (c *Client) do(command string) ([]byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	deadline := time.Now().Add(timeout)
	name, err := windows.UTF16PtrFromString(c.PipeName)
	if err != nil {
		return nil, err
	}

	for {
		waitResult, _, _ := procWaitNamed.Call(uintptr(unsafe.Pointer(name)), 50)
		if waitResult == 0 {
			if time.Now().After(deadline) {
				return nil, errors.New("SkyBootstrap pipe is not available; is the game running?")
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}

		handle, err := windows.CreateFile(name,
			windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			if errors.Is(err, windows.ERROR_PIPE_BUSY) {
				if time.Now().After(deadline) {
					return nil, errors.New("SkyBootstrap pipe is busy")
				}
				time.Sleep(25 * time.Millisecond)
				continue
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("open SkyBootstrap pipe: %w", err)
			}
			time.Sleep(25 * time.Millisecond)
			continue
		}

		response, err := c.roundTrip(handle, []byte(command), timeout)
		windows.CloseHandle(handle)
		return response, err
	}
}

func (c *Client) roundTrip(handle windows.Handle, command []byte, timeout time.Duration) ([]byte, error) {
	mode := uint32(windows.PIPE_READMODE_MESSAGE)
	if err := windows.SetNamedPipeHandleState(handle, &mode, nil, nil); err != nil {
		return nil, fmt.Errorf("set pipe message mode: %w", err)
	}

	timer := time.AfterFunc(timeout, func() {
		procCancelIOEx.Call(uintptr(handle), 0)
	})
	defer timer.Stop()

	var written uint32
	if err := windows.WriteFile(handle, command, &written, nil); err != nil {
		return nil, fmt.Errorf("write pipe command: %w", err)
	}
	if int(written) != len(command) {
		return nil, errors.New("short write to SkyBootstrap pipe")
	}

	buffer := make([]byte, 8<<20)
	var read uint32
	if err := windows.ReadFile(handle, buffer, &read, nil); err != nil {
		return nil, fmt.Errorf("read SkyBootstrap response: %w", err)
	}
	return parseResponse(buffer[:read])
}
