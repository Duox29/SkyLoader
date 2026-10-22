# SkyLoader

SkyLoader manages a local library of plugin bundles for Sky and injects them
into a running game session.

## Plugins

**Plugin bundle**:
A directory or zip containing `plugin.json`, exactly one DLL, and an optional
prebuilt web UI.
_Avoid_: mod package, plugin folder

**Plugin manifest**:
The `plugin.json` document that declares a bundle's identity, display metadata,
DLL, and UI entry.
_Avoid_: sidecar, VERSIONINFO (that is legacy metadata)

**Plugin identity**:
The permanent, case-insensitive `id` that identifies one plugin across renamed
files, bundles, and releases.
_Avoid_: filename, display name

**Managed plugin**:
A plugin bundle copied into SkyLoader's managed plugin store and referenced by
`config.json`.
_Avoid_: source DLL, registered path

**Legacy plugin**:
A DLL registered by absolute path outside the managed store, either imported
before bundles existed or selected directly.
_Avoid_: unmanaged plugin, external mod

**Plugin name**:
The human-readable `name` shown in SkyLoader's plugin list.
_Avoid_: plugin identity, filename

**Plugin author**:
The human-readable `author` shown in SkyLoader's plugin list.
_Avoid_: plugin identity, publisher key

**Plugin description**:
The human-readable `description` shown in SkyLoader's plugin list.
_Avoid_: file description, plugin identity

## Runtime

**Launcher**:
The `SkyLoader.exe` desktop application that owns settings, the managed store,
the process watcher, injection, the local UI server, and updates.

**Bootstrap host**:
The `SkyBootstrap.dll` Go shared library loaded inside `Sky.exe`; it owns the
named pipe, plugin DLL lifecycle, and per-plugin UI queues.
_Avoid_: kernel, injector

**Plugin UI**:
A prebuilt web page shipped inside a plugin bundle and shown in the launcher's
iframe or a browser.
_Avoid_: overlay, in-game menu

**UI bridge**:
The injected `window.sky` JavaScript API that carries plugin UI requests and
events between the page and the plugin DLL.
_Avoid_: IPC, RPC (those name the transport layers)

**UI message**:
One JSON request/response exchanged with a plugin through its optional
`SkyPluginUiMessage` export.
_Avoid_: packet, command

**Plugin event**:
A JSON value queued by a plugin with `post_event` and delivered to its UI by
the bridge.
_Avoid_: notification, callback

## Settings

**Config file**:
`%LOCALAPPDATA%\SkyLoader\config.json`, the current persistent settings format.
_Avoid_: INI, registry

**Legacy settings**:
An old `SkyLoader.ini` next to the launcher executable, imported once when no
config file exists.
_Avoid_: config, settings file
