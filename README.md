# SkyLoader

SkyLoader is a Windows launcher, injector, and plugin manager for **Sky: Children
of the Light**. It watches for `Sky.exe`, injects a small in-game host, and
loads plugin DLLs. Plugins can ship a prebuilt web UI that SkyLoader displays in
its own window or opens in a browser.

The entire application is written in Go. There is no C or C++ source in this
repository and no in-game Direct3D overlay.

---

## Architecture

```text
SkyLoader.exe (Go + Wails desktop UI)
├── settings, process watcher, Steam launch
├── managed plugin store and bundle import
├── local web-UI server + JavaScript bridge
├── update check
└── injects SkyBootstrap.dll into Sky.exe

Sky.exe
└── SkyBootstrap.dll (Go, c-shared)
    ├── named pipe \\.\pipe\sky_bootstrap
    ├── plugin DLL loading and unloading
    └── per-plugin UI request/event queues
```

Plugin web UIs are not rendered inside the game. They are served on a random
loopback port, protected by a random token in the URL, and shown in SkyLoader's
in-app iframe. **Open in browser** uses the same authenticated URL.

---

## Build

Requirements:

- Windows (64-bit)
- Go 1.22 or newer on `PATH`
- Zig 0.13 or newer on `PATH` (used as the cgo compiler for the DLL)

Run `build.bat` from the repository root to open the interactive menu:

```bat
build.bat
```

Choose **Build launcher (Go)**, **Build DLL (Zig)**, **Build all**, or **Clean**.
You can also build directly from Command Prompt without opening the menu:

```bat
build.bat launcher
build.bat dll
build.bat all
```

To remove build outputs and the generated launcher resource object:

```bat
build.bat clean
```

Outputs:

```text
dist\SkyLoader.exe
dist\SkyBootstrap.dll
```

`build.bat launcher` compiles the icon and manifest with pure Go and builds the
Wails app with `CGO_ENABLED=0`. `build.bat dll` builds the c-shared DLL with
cgo and uses `zig cc -target x86_64-windows-gnu` as the C compiler. Set `ZIG`
to point at a different Zig executable:

```bat
set "ZIG=C:\tools\zig\zig.exe"
build.bat dll
```

The `GO` environment variable can similarly point to a different `go.exe`.
If Go or Zig was just installed, open a new terminal so it picks up the updated
`PATH`. Git Bash, MSYS2, and a separate system C toolchain are not required.

Create the installer after building:

```bat
build.bat all
iscc installer\SkyLoader.iss
```

This requires [Inno Setup](https://jrsoftware.org/isinfo.php) (`iscc`) on
`PATH`.

---

## Use

1. Run `SkyLoader.exe`.
2. Confirm the `Sky.exe` and `SkyBootstrap.dll` paths. SkyLoader auto-detects
   Steam libraries and falls back to the bundled bootstrap DLL.
3. Add plugins by dragging a `.dll`, `.zip` bundle, or bundle folder onto the
   window, or use **Add plugins**.
4. Click **Launch Sky**. SkyLoader asks Steam to start the game, waits for
   `Sky.exe`, injects SkyBootstrap, and loads enabled plugins.
5. Open a plugin's UI with **Open**, or use **Open in browser**.

Sky started directly from Steam is detected too; leave **Auto inject** enabled
to load plugins automatically. **Inject** retries loading into a running game.

SkyLoader checks the latest GitHub release in the background and on demand. A
new installer is downloaded to `%LOCALAPPDATA%\SkyLoader\updates` and can be run
from the update banner.

---

## Files and settings

SkyLoader keeps user data outside the installation directory:

```text
%LOCALAPPDATA%\SkyLoader\
├── config.json          settings, plugin list, enabled state
├── plugins\<id>\        managed plugin bundles
├── logs\SkyLoader.log   launcher log
├── logs\SkyBootstrap.log in-game host log
├── updates\             downloaded installers
└── webview\             WebView2 profile
```

`config.json` replaces the old `SkyLoader.ini`. On first run, a legacy
`SkyLoader.ini` next to `SkyLoader.exe` is imported once. `SkyOverlay.dll`
entries are removed during that migration because the overlay no longer exists.
Plugins are now stored as bundles under `plugins\`, so existing DLL paths are
re-registered as legacy plugins until they are re-imported.

If a plugin fails to load, check both log files. Both the launcher and the
in-game host must be 64-bit, and the configured game path must be the running
`Sky.exe`.

---

## Plugin bundles

A bundle is a directory or zip file containing `plugin.json`, one DLL, and an
optional `ui/` directory:

```text
example-plugin/
├── plugin.json
├── example-plugin.dll
└── ui/
    ├── index.html
    ├── app.js
    └── style.css
```

A minimal manifest:

```json
{
  "id": "example.author.example-plugin",
  "name": "Example Plugin",
  "version": "1.0.0",
  "author": "Example Author",
  "description": "Example SkyLoader plugin",
  "dll": "example-plugin.dll",
  "ui": "ui"
}
```

The `id` is the permanent, case-insensitive package identity. It may contain
letters, digits, `.`, `_`, and `-`. If it is omitted, SkyLoader reads
`SkyPluginId` from the DLL's Windows `VERSIONINFO`, then falls back to the
filename. A manifest ID must match the DLL's `SkyPluginId` when both exist.

`dll` and `ui` are optional when the bundle contains exactly one DLL and a
standard `ui/index.html`. `uiEntry` can point at another HTML file inside the
UI directory. Paths may not escape the bundle.

Single DLLs still work: SkyLoader synthesizes a bundle, copies a sidecar
`<name>.ui` or `ui` folder when present, and reads `VERSIONINFO` metadata.
Re-importing the same `id` replaces the managed bundle after version checks;
lower versions are rejected unless the import is explicitly forced.

The plugin ABI is defined in
[`libraries/SkyBootstrap/include/skybootstrap_api.h`](libraries/SkyBootstrap/include/skybootstrap_api.h).
Every plugin exports `SkyPluginInit`. `SkyPluginShutdown`, `SkyPluginUiMessage`,
and the `post_event` callback are optional. See
[Creating a SkyLoader plugin](docs/creating-a-plugin.md) for the complete ABI,
manifest schema, web bridge, and build example.

Ready-to-run sample bundles — one per data pattern (scalar fields, ordered
lists, nested structs with dot paths and pushed events) plus detailed guides —
live in [`plugins/`](plugins/README.md).

### Web UI bridge

SkyLoader injects `window.sky` into every plugin page:

```js
await window.sky.call({ action: "status" }); // JSON request -> JSON response
const off = window.sky.onEvent((event) => console.log(event));
window.sky.openExternal("https://example.com");
window.sky.close();
```

`call` reaches the plugin's exported `SkyPluginUiMessage` through the launcher,
the named pipe, and the in-game host. `onEvent` receives JSON pushed by the
plugin with `api->post_event`. The bridge is only live while Sky is running
with the plugin loaded. Relative assets work because the server injects a
`<base>` element; do not hard-code absolute paths.

---

## Repository layout

```text
build.bat             Windows build menu and command-line targets
cmd/skyloader/        Wails launcher and embedded frontend
cmd/skybootstrap/     c-shared DLL entry points and in-game host
internal/launcher/    settings, plugin store, watcher, injection, updater
internal/uiserver/    authenticated loopback server, JS bridge, pipe protocol
libraries/            public plugin ABI header
plugins/              sample plugin bundles, shared kit, and plugin guides
resources/            launcher icon, manifest, and resource generator
installer/            Inno Setup script
```

---

## Credits

SkyLoader builds on ideas and research from the Sky PC modding community:

- **Lukas (`sml-pc`)**: original Sky mod loader research, early injection
  concepts, and DLL proxy architecture.
- **XeTrinityz (`ThatSkyLoader`)**: rapid plugin UI concepts and community
  modding ergonomics.
- **MrGatto**: named-pipe IPC design and remote injection patterns.
- **HerokeyVN**: loader design, plugin orchestration, and the Go rewrite.

<p align="center">
  Made with ❤️ for the Sky: Children of the Light community.
</p>
