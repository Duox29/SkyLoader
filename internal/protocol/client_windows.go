//go:build windows

package protocol

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

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
