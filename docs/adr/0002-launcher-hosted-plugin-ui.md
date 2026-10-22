# Launcher-hosted plugin web UIs

## Context

The previous architecture shipped `SkyOverlay.dll`, a C++ Direct3D 11 + Dear
ImGui overlay loaded next to plugins inside Sky. It required a separate native
build, ImGui sources, render hooks, and session bookkeeping (ADR 0001). The game
itself renders with Vulkan, so the overlay was a second presentation path that
had to be kept in sync with window and swapchain state.

SkyLoader is moving to a Go-only codebase and must not require a system C or
C++ toolchain. Plugin authors also need a way to build rich interfaces without
linking ImGui or sharing the game's render thread.

## Decision

Remove SkyOverlay and the in-game overlay entirely.

Plugins may ship prebuilt static web assets in their bundle. The launcher serves
those assets on `127.0.0.1:<random-port>` with an unguessable token in every
path, renders the entry page in an in-app iframe, and offers the same URL in the
default browser.

The iframe gets a `window.sky` bridge that:

- sends JSON requests to the plugin DLL's optional `SkyPluginUiMessage` export
  through the launcher and the existing named pipe;
- receives JSON values queued by the DLL through `post_event`;
- asks the launcher to close the UI or open an external link.

SkyBootstrap becomes a Go `c-shared` DLL. Plugin ABI 4 keeps the version-3
`version`/`log` prefix, adds `post_event`/`event_context`, and defines
`SkyPluginUiMessage`. The launcher and bootstrap communicate over the named
pipe; the launcher never loads plugin DLLs into its own process.

Zig is used only as the cgo C compiler for the Windows DLL build. It is a
build-time dependency (provided on `PATH` or by CI), not a runtime or
user-facing requirement.

## Consequences

- There is no in-game overlay to hook, resize, or synchronize with Vulkan.
- Plugin UIs live outside the game window. They work while Sky is running but
  require the bootstrap host to be injected for bridge calls and events.
- Plugin authors can use any web toolchain and ship static files; no ImGui or
  C++ ABI is imposed.
- Plugins without a UI and existing ABI-3 plugins remain supported.
- The launcher owns authentication, asset serving, and the bridge, so plugin
  UI code is treated as local code with the launcher's privileges.
- `SkyOverlay.dll` entries are dropped when legacy `SkyLoader.ini` settings are
  migrated.
- ADR 0001 (overlay session registry) is superseded and kept for history.
