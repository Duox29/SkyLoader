# Writing the plugin DLL

This page walks the native half of a plugin: what the host calls, when it
calls it, and how to build a DLL that survives all of it. The ABI reference is
[`libraries/SkyBootstrap/include/skybootstrap_api.h`](../../libraries/SkyBootstrap/include/skybootstrap_api.h)
(ABI 4); the samples share a small header kit in
[`plugins/examples/common/skyplugin.h`](../examples/common/skyplugin.h).

## 1. What happens at load time

When Sky starts from SkyLoader, the launcher injects `SkyBootstrap.dll` into
`Sky.exe`. The host owns a named pipe; when the launcher asks it to load a
plugin the sequence is:

```text
LOAD C:\...\plugins\<id>\example.dll        (pipe command from the launcher)
  → LoadLibraryEx(path, DLL search: load dir + defaults)
  → GetProcAddress: SkyPluginInit (required), SkyPluginShutdown, SkyPluginUiMessage
  → SkyPluginInit(&SkyBootstrapApi)         on a bootstrap worker thread
  → init returns 0?  → FreeLibrary, plugin not registered
  → init returns 1?  → plugin is live until UNLOAD/close-Sky
```

Everything after `SkyPluginInit` is negotiated through the `SkyBootstrapApi`
structure that was passed in. The host keeps the pointer valid for the plugin's
whole lifetime; store it once and use it everywhere.

## 2. The `SkyBootstrapApi` structure

```c
typedef struct SkyBootstrapApi {
  uint32_t version;        // ABI version of the host (4)
  SkyBootstrapLogFn log;   // write a UTF-8 line into SkyBootstrap.log
  SkyBootstrapPostEventFn post_event;  // NULL on hosts older than ABI 4
  void *event_context;     // opaque value to pass as post_event's first arg
} SkyBootstrapApi;
```

- `log(message)`: line-oriented logging, `SkyBootstrap.log` in
  `%LOCALAPPDATA%\SkyLoader\logs\`. Cheap and worker-thread-safe; use it for
  init, errors, and state transitions, not per-frame traffic.
- `post_event(context, json)`: queue one JSON event for this plugin's web UI.
  Pass `api->event_context` as the context argument. Returns `1` when the
  event was accepted. The queue is per plugin with hard caps — 256 events,
  1 MiB total, 64 KiB per event — full queues drop silently, and nothing is
  persisted across sessions.

Version policy: check `api->version` first. The kit's rule is a good default —
reject hosts older than the ABI you actually need, accept anything newer:

```cpp
if (api == nullptr || api->version < SKYBOOTSTRAP_API_VERSION) return 0;
```

And handle `post_event == nullptr` gracefully so a host without the callback
still loads you (the sample kit logs a warning and drops events).

## 3. The three exports

| Export | Required | Runs on | Contract |
| --- | --- | --- | --- |
| `SkyPluginInit(const SkyBootstrapApi*)` | yes | bootstrap worker thread | Return non-zero to accept the host. Must return quickly. |
| `SkyPluginShutdown()` | no | bootstrap worker thread | Releases threads/hooks/sockets before the DLL is freed. |
| `SkyPluginUiMessage(const char* request, char* response, uint32_t capacity)` | no | bootstrap worker thread | Handles one JSON UI message and writes a JSON reply. |

```c
typedef int32_t (SKYBOOTSTRAP_CALL *SkyPluginUiMessageFn)(
    const char *request_json,       // NUL-terminated UTF-8 JSON request
    char *response_json,            // host-provided buffer, 4 MiB
    uint32_t response_capacity);
```

Return values of `SkyPluginUiMessage`:

- `>= 0` — the number of bytes written into `response_json` (not counting the
  NUL you must also write). The host passes the bytes back as the HTTP body.
- `0` — intentionally empty reply; the page's `window.sky.call` resolves to
  `null`.
- `< 0` — failure; the launcher answers HTTP 502 with `{"error": ...}` and the
  page sees a thrown error. Reserve this for calls that never made sense;
  application-level errors belong inside the JSON envelope instead.

Threading model in one paragraph: UI requests arrive on a bootstrap worker
thread, and the host serializes calls per plugin (one at a time, and unload
waits until an in-flight call finishes before calling shutdown). But **that
thread is not the game's render thread**. Anything touching game memory or
game-owned resources must be marshaled to the correct game thread; the UI
thread should only read plugin-owned state and return. Never block on it:
the launcher, the HTTP bridge, and the pipe all sit in the path.

## 4. The minimal plugin (raw ABI)

This is a complete, buildable plugin. Ten lines: adopt the host, log, unload.

```cpp
#include <windows.h>
#include "skybootstrap_api.h"

static const SkyBootstrapApi* g_api = nullptr;

extern "C" __declspec(dllexport)
int __stdcall SkyPluginInit(const SkyBootstrapApi* api) {
  if (!api || api->version < SKYBOOTSTRAP_API_VERSION) return 0;
  g_api = api;
  api->log("Minimal plugin initialized.");
  return 1;
}

extern "C" __declspec(dllexport)
void __stdcall SkyPluginShutdown() {
  if (g_api && g_api->log) g_api->log("Minimal plugin unloaded.");
  g_api = nullptr;
}
```

That is the floor of the platform. Everything else — payloads, envelopes,
events — is your design on top of `SkyPluginUiMessage` and `post_event`.

## 5. The shape a real plugin uses (the kit path)

Every sample follows the same skeleton with the kit header:

```cpp
#include "skyplugin.h"   // resolves libraries/SkyBootstrap/include + common/

namespace {
void handleRequest(const skyplugin::Value& request, skyplugin::Value& response) {
  const std::string action = request.stringOr("action", "");
  // ... read request, validate, build the reply envelope ...
  response = skyplugin::okReply(state);
}
}  // namespace

SKYPLUGIN_EXPORT int SKYBOOTSTRAP_CALL SkyPluginInit(const SkyBootstrapApi* api) {
  if (!skyplugin::init(api, "My plugin")) return 0;
  skyplugin::setHandler(&handleRequest);
  return 1;
}

SKYPLUGIN_EXPORT void SKYBOOTSTRAP_CALL SkyPluginShutdown() {
  skyplugin::shutdown();
}

SKYPLUGIN_EXPORT int32_t SKYBOOTSTRAP_CALL SkyPluginUiMessage(
    const char* request_json, char* response_json, uint32_t response_capacity) {
  return skyplugin::dispatch(request_json, response_json, response_capacity);
}
```

What `skyplugin::dispatch` gives you: JSON parsing with error envelopes, a
fixed `{"v":1,"ok":true,"data":...}` / `{"v":1,"ok":false,"error":{...}}`
envelope, `UNKNOWN_ACTION` for unmatched actions (leave `response` null),
exception-to-envelope mapping (`PLUGIN_ERROR`), and capacity checking with a
negative return for oversized replies. The kit's data types and helpers:
`skyplugin::Value` (JSON value), `parse`/`write`, `okReply`/`errorReply`,
`postEvent`, `log`, `init`/`shutdown`.

The kit is intentionally tiny (one header, no dependencies): copy
`plugins/examples/common/skyplugin.h` into your plugin repository and bend it.

## 6. Building the DLL

Any toolchain that emits a 64-bit DLL with undecorated export names works. All
commands below include SkyLoader's public header path
(`-I libraries/SkyBootstrap/include`) and for the kit (`-I plugins/examples/common`).

MSVC (run inside an **x64 Native Tools Command Prompt**):

```bat
cl /nologo /LD /EHsc /std:c++17 /O2 ^
  /I"libraries\SkyBootstrap\include" ^
  /I"plugins\examples\common" ^
  plugins\examples\01-field-settings\field-settings.cpp ^
  /Fe:field-settings.dll
```

MinGW-w64:

```sh
g++ -std=c++17 -O2 -shared -static -static-libgcc -static-libstdc++ \
  -I libraries/SkyBootstrap/include -I plugins/examples/common \
  plugins/examples/01-field-settings/field-settings.cpp \
  -o field-settings.dll
```

Zig (what SkyLoader itself uses to cross-compile from any OS):

```sh
zig c++ -std=c++17 -O2 -shared -target x86_64-windows-gnu \
  -I libraries/SkyBootstrap/include -I plugins/examples/common \
  plugins/examples/01-field-settings/field-settings.cpp \
  -o field-settings.dll
```

Notes:

- The DLL must be 64-bit; Sky's process will not load a 32-bit DLL.
- Static linking of the C++ runtime (`-static`) keeps plugins free of
  `libstdc++`/`libgcc` DLL dependencies; with MSVC prefer `/MT` if you do not
  want users to need the VC++ redistributable.
- No `.def` file is needed: on x64 there is no `__stdcall` name decoration and
  the host resolves exports by plain name.
- The samples ship ready-made build scripts:
  `plugins/examples/build.cmd` (Windows) and `plugins/examples/build.sh`
  (cross, uses Zig or MinGW).

## 7. Pitfall checklist

1. Returning `0` from init logs nothing. Log *why* before returning zero.
2. Shutdown leaks: join your threads, free your hooks, then return. The DLL
   is `FreeLibrary`-ed right after.
3. Blocking the UI message thread stalls every future call and the launcher's
   bridge. Marshal slow work; keep handlers fast.
4. Calling game functions from the bootstrap worker thread. Wrong thread,
   wrong locks, crash. First get plugin-owned state right; touch the game only
   via a path you have proven on the game thread.
5. Oversized replies: one reply never exceeds the 4 MiB buffer (and the HTTP
   side only accepts 2 MiB *requests*). See
   [`04-data-patterns.md`](04-data-patterns.md) for payload strategies.
6. Assuming events persist: they are RAM-only, dropped when queues fill, and
   gone when Sky closes.
