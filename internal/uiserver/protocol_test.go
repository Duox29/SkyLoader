//go:build windows

package uiserver

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestUICommandRoundTrip(t *testing.T) {
	payload := []byte(`{"action":"ping","nested":{"value":1}}`)
	command := UICommand(`C:\Mods\My Plugin\plugin.dll`, payload)
	prefix := `UI C:\Mods\My Plugin\plugin.dll `
	if !strings.HasPrefix(command, prefix) {
		t.Fatalf("command = %q", command)
	}
	encoded := strings.TrimPrefix(command, prefix)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(decoded) != string(payload) {
		t.Fatalf("decoded = %q, %v", decoded, err)
	}
}

func TestParseResponse(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
		code    string
	}{
		{name: "empty", input: "OK\n"},
		{name: "data", input: "OK {\"ok\":true}\n", want: `{"ok":true}`},
		{name: "failure", input: "FAIL LOAD_FAILED plugin missing\n", wantErr: true, code: "LOAD_FAILED"},
		{name: "unexpected", input: "WHAT\n", wantErr: true},
		{name: "blank", input: "\n", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data, err := parseResponse([]byte(test.input))
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", data)
				}
				if test.code != "" {
					remote, ok := err.(*RemoteError)
					if !ok || remote.Code != test.code {
						t.Fatalf("error = %#v", err)
					}
				}
				return
			}
			if err != nil || string(data) != test.want {
				t.Fatalf("data = %q, err = %v", data, err)
			}
		})
	}
}

func TestDecodeResponses(t *testing.T) {
	ui, err := DecodeUIResponse([]byte(base64.StdEncoding.EncodeToString([]byte(`{"x":1}`))))
	if err != nil || string(ui) != `{"x":1}` {
		t.Fatalf("UI response = %q, %v", ui, err)
	}
	events, err := DecodePollResponse([]byte(`[{"type":"one"},2,"three"]`))
	if err != nil || len(events) != 3 || string(events[1]) != "2" {
		t.Fatalf("events = %q, %v", events, err)
	}
	paths, err := DecodeListResponse([]byte(`["C:\\a.dll","C:\\b.dll"]`))
	if err != nil || len(paths) != 2 || paths[0] != `C:\a.dll` {
		t.Fatalf("paths = %q, %v", paths, err)
	}
}
