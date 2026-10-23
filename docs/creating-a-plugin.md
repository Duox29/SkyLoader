# Creating a SkyLoader plugin

A SkyLoader plugin is a 64-bit Windows DLL plus an optional prebuilt web UI.
The launcher injects `SkyBootstrap.dll` into Sky, and the bootstrap host loads
the plugin DLL from inside the game process. Plugin UIs run outside the game in
SkyLoader's window or a browser and talk to the DLL through a JSON bridge.

Runnable sample bundles and deeper guides (bundle lifecycle, UI bridge
internals, field/list/struct payload patterns, testing) live in
[`plugins/`](../plugins/README.md). This document remains the canonical ABI
and manifest reference.

The ABI is C-compatible and defined in
[`libraries/SkyBootstrap/include/skybootstrap_api.h`](../libraries/SkyBootstrap/include/skybootstrap_api.h).
It works from C, C++, Rust, Zig, or any toolchain that produces an x64 DLL.

## 1. Bundle layout

Package the plugin as a folder or zip:

```text
example-plugin/
├── plugin.json
├── example-plugin.dll
└── ui/
    ├── index.html
    ├── app.js
    └── style.css
```

`plugin.json` is optional for a bare DLL, but a bundle manifest is the
recommended format because it carries the stable identity and UI entry.

```json
{
  "id": "example.author.example-plugin",
  "name": "Example Plugin",
  "version": "1.0.0",
  "author": "Example Author",
  "description": "Example SkyLoader plugin",
  "dll": "example-plugin.dll",
  "ui": "ui",
  "uiEntry": "index.html"
}
```

| Field | Meaning |
| --- | --- |
| `id` | Permanent package identity. Case-insensitive; letters, digits, `.`, `_`, `-`; at most 128 characters. |
| `name` | Display name. Falls back to `PluginName`, then the DLL filename. |
| `version` | Loose version string used for update ordering. Falls back to `Version`. |
| `author` | Display author. Falls back to `Author` or `CompanyName`. |
| `description` | Display description. Falls back to `Description` or `FileDescription`. |
| `dll` | DLL path relative to the bundle root. Optional when the root has exactly one DLL. |
| `ui` | UI directory relative to the bundle root. Defaults to `ui` when it contains `index.html`. |
| `uiEntry` | Entry HTML file, relative to the UI directory or the bundle root. Defaults to `index.html`. |

Paths must stay inside the bundle. A zip may contain the bundle files at its
root or inside one top-level folder. The managed copy is stored under
`%LOCALAPPDATA%\SkyLoader\plugins\<id>\`.

If `id` is omitted, SkyLoader reads `SkyPluginId` from the DLL's Windows
`VERSIONINFO` resource, then falls back to a filename-derived identity. When
both `plugin.json` and `VERSIONINFO` declare an identity, they must match. A
single imported DLL also picks up a neighboring `<name>.ui` or `ui` folder and
synthesizes a manifest automatically.

## 2. Plugin exports

Every plugin must export:

```cpp
extern "C" __declspec(dllexport)
int __stdcall SkyPluginInit(const SkyBootstrapApi* api);
```

Return non-zero to accept the API. Return zero to reject it; the host will not
load the plugin. `SkyPluginInit` runs on a bootstrap worker thread, not the
game's render thread. It must return quickly.

Optional exports:

```cpp
extern "C" __declspec(dllexport)
void __stdcall SkyPluginShutdown();

extern "C" __declspec(dllexport)
int32_t __stdcall SkyPluginUiMessage(const char* request_json,
                                     char* response_json,
                                     uint32_t response_capacity);
```

`SkyPluginShutdown` runs before the DLL is unloaded. Release threads, hooks,
and other resources there.

The `SkyBootstrapApi` structure provides:

- `version`: host ABI version. This document describes ABI 4.
- `log(message)`: writes a UTF-8 line to `SkyBootstrap.log`.
- `post_event(context, json)`: queues a JSON event for the plugin UI. Pass
  `api->event_context` as `context`. Returns 1 when accepted.
- `event_context`: opaque host-owned value for `post_event`.

Always check `api->version` before using ABI 4 features, and check that
`post_event` is not null when older hosts may load the plugin.

```cpp
#include <windows.h>
#include "skybootstrap_api.h"

static const SkyBootstrapApi* gApi = nullptr;

extern "C" __declspec(dllexport)
int __stdcall SkyPluginInit(const SkyBootstrapApi* api) {
  if (!api || api->version < 4) return 0;
  gApi = api;
  api->log("Example plugin initialized.");
  api->post_event(api->event_context, "{\"type\":\"ready\"}");
  return 1;
}

extern "C" __declspec(dllexport)
void __stdcall SkyPluginShutdown() {
  if (gApi && gApi->log) gApi->log("Example plugin shutting down.");
  gApi = nullptr;
}
```

## 3. Handle UI requests

When a plugin UI calls `window.sky.call(payload)`, SkyLoader sends the JSON
payload to the plugin's `SkyPluginUiMessage` export. The request is
NUL-terminated. The host provides a response buffer of
`SKYBOOTSTRAP_UI_MESSAGE_LIMIT` (4 MiB), including room for the NUL terminator.

Write a NUL-terminated JSON response and return its byte length **excluding**
the terminator. Return `0` for an empty response and a negative value to report
an error. The response must be valid JSON; otherwise SkyLoader wraps it as
`{"raw": "..."}` for the page. The launcher's HTTP bridge accepts request
payloads up to 2 MiB, below the ABI's 4 MiB transport limit, so keep UI requests
small.

```cpp
#include <cstdint>
#include <cstring>

extern "C" __declspec(dllexport)
int32_t __stdcall SkyPluginUiMessage(const char* request_json,
                                     char* response_json,
                                     uint32_t response_capacity) {
  if (!request_json || !response_json || response_capacity == 0) return -1;

  const char* body = nullptr;
  if (std::strstr(request_json, "\"action\":\"ping\"")) {
    body = "{\"ok\":true,\"reply\":\"pong\"}";
  } else {
    body = "{\"ok\":false,\"error\":\"unknown action\"}";
  }

  const size_t length = std::strlen(body);
  if (length + 1 > response_capacity) return -1;
  std::memcpy(response_json, body, length + 1);
  return static_cast<int32_t>(length);
}
```

UI requests arrive on a bootstrap worker thread. Keep handlers fast and
thread-safe. If a handler needs to touch game state, marshal the work to the
appropriate game thread and return; do not block the pipe for long periods.

`post_event` accepts one JSON value per call. Events are queued per plugin
(maximum 256 events, 1 MiB total, 64 KiB per event); when the queue is full,
the event is dropped. Invalid JSON is delivered as a JSON string. Events are
not persisted across game sessions.

## 4. Build the DLL

Build an x64 DLL. A 32-bit DLL cannot load into Sky's 64-bit process.

With MSVC:

```bat
cl /nologo /LD /O2 /EHsc /std:c++17 ^
  /I"libraries\SkyBootstrap\include" ^
  plugin.cpp /Fe:example-plugin.dll
```

With MinGW or clang:

```sh
g++ -std=c++17 -O2 -shared \
  -I../SkyLoader/libraries/SkyBootstrap/include \
  plugin.cpp -o example-plugin.dll
```

An optional `VERSIONINFO` resource remains useful for bare DLL imports and
legacy tools:

```rc
VS_VERSION_INFO VERSIONINFO
 FILEVERSION 1,0,0,0
 PRODUCTVERSION 1,0,0,0
 FILEFLAGSMASK 0x3fL
 FILEFLAGS 0x0L
 FILEOS VOS__WINDOWS32
 FILETYPE VFT_DLL
 FILESUBTYPE VFT2_UNKNOWN
BEGIN
  BLOCK "StringFileInfo"
  BEGIN
    BLOCK "040904B0"
    BEGIN
      VALUE "SkyPluginId", "example.author.example-plugin\0"
      VALUE "PluginName", "Example Plugin\0"
      VALUE "Version", "1.0.0\0"
      VALUE "Author", "Example Author\0"
      VALUE "Description", "Example SkyLoader plugin\0"
    END
  END
  BLOCK "VarFileInfo"
  BEGIN
    VALUE "Translation", 0x0409, 1200
  END
END
```

## 5. Build the web UI

The UI is plain static HTML, CSS, and JavaScript. SkyLoader serves it from a
random loopback port with an authorization token in the URL and injects
`window.sky` into every HTML page. Relative URLs use an injected `<base>` tag,
so keep assets relative. Do not include your own bridge script.

`ui/index.html`:

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>Example Plugin</title>
  <style>
    body { font: 14px system-ui; background: #0b1424; color: #e6eefc; padding: 24px; }
    button { padding: 8px 14px; }
  </style>
</head>
<body>
  <h1>Example Plugin</h1>
  <button id="ping">Ping plugin</button>
  <pre id="output"></pre>

  <script>
    const output = document.getElementById("output");

    document.getElementById("ping").addEventListener("click", async () => {
      try {
        const reply = await window.sky.call({ action: "ping" });
        output.textContent = JSON.stringify(reply, null, 2);
      } catch (error) {
        output.textContent = "Error: " + error.message;
      }
    });

    window.sky.onEvent((event) => {
      output.textContent += "\nevent: " + JSON.stringify(event);
    });
  </script>
</body>
</html>
```

The bridge API:

- `window.sky.plugin`: `{ id, name, version }`.
- `window.sky.call(payload)`: sends a JSON request and resolves with the
  parsed response. Throws on transport or plugin errors.
- `window.sky.onEvent(listener)`: subscribes to plugin-pushed events and
  returns an unsubscribe function.
- `window.sky.close()`: asks SkyLoader to close the in-app UI.
- `window.sky.openExternal(url)`: asks SkyLoader to open an `http`/`https` URL
  in the default browser.

Plugins can ship a Vite, React, Svelte, or other prebuilt bundle. SkyLoader
does not run npm or a dev server; build the static files before packaging.
Avoid absolute paths such as `/assets/app.js`; use relative paths or the
authenticated base URL already injected by the server.

## 6. Import and update

Drag a `.dll`, `.zip`, or bundle folder into SkyLoader, or use **Add plugins**.
The bundle is copied into the managed store. Re-importing the same `id`
replaces the managed bundle:

- A higher version replaces the installed one.
- An equal version is accepted, which supports rebuilt artifacts.
- A lower version is rejected unless the import is explicitly confirmed with
  force.
- If metadata exists on only one side, SkyLoader asks before replacing.

If Sky has the old DLL loaded, Windows may keep it locked. Close Sky before
importing an update. Enabling or disabling a plugin while Sky is running loads
or unloads it immediately when the bootstrap host is active.

Legacy DLLs registered by absolute path keep working, but their files are not
deleted by SkyLoader and they do not get a managed bundle directory.

## 7. Test checklist

1. Import the bundle and verify name, version, author, description, and the
   **UI** badge appear in SkyLoader.
2. Launch Sky from SkyLoader and confirm `SkyBootstrap.log` contains the
   initialization message.
3. Open the UI, call the plugin, and verify the JSON response.
4. Push an event with `post_event` and verify `onEvent` receives it.
5. Disable and enable the plugin while Sky is running and verify it unloads
   and reloads.
6. Import a newer build with the same `id` and a different filename; verify one
   entry remains.
7. Try importing an older version and verify SkyLoader keeps the newer one.
8. Remove the plugin while Sky is closed and verify shutdown cleanup and the
   managed directory removal.
