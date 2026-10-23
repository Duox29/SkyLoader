// Package uiserver serves plugin UI bundles from the local machine, bridges
// their JavaScript to the in-game plugin DLL, and implements the named-pipe
// contract with SkyBootstrap.
package uiserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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
