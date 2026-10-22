# Plugin anatomy: bundle, manifest, and the managed store

Before a plugin can talk to a UI, it has to exist as something SkyLoader can
ship, identify, update, and load. This guide covers the on-disk anatomy. The
canonical contract is
[`docs/creating-a-plugin.md`](../../docs/creating-a-plugin.md); this page adds
the reasoning and points at runnable samples in [`plugins/examples/`](../examples/).

## 1. What a plugin is

A plugin is a **bundle**: one x64 Windows DLL plus an optional prebuilt web UI
and a manifest.

```text
plugins/examples/01-field-settings/
├── plugin.json            manifest (identity + pointers)
├── field-settings.dll     the plugin (built with build.cmd / build.sh)
└── ui/                    optional prebuilt web UI
    ├── index.html         entry point (uiEntry)
    ├── app.js             page code, talks through window.sky
    └── style.css
```

SkyLoader never builds your code. Ship DLLs, ship `index.html`, and SkyLoader
does the rest: copying, versioning, injection, hosting, and serving.

## 2. The manifest (`plugin.json`)

The manifest is the identity document of the bundle:

```json
{
  "id": "skyloader.sample.field-settings",
  "name": "Field Settings (sample)",
  "version": "1.0.0",
  "author": "SkyLoader Samples",
  "description": "Sample plugin teaching scalar field get/set/reset over the UI bridge.",
  "dll": "field-settings.dll",
  "ui": "ui",
  "uiEntry": "index.html"
}
```

| Field | Meaning |
| --- | --- |
| `id` | Permanent package identity. Letters, digits, `.`, `_`, `-`; at most 128 characters; case-insensitive. Never rename it between releases. |
| `name` | Display name in the launcher list. |
| `version` | Loose version string, used by the update ordering. |
| `author` | Display author. |
| `description` | Display description. |
| `dll` | DLL path relative to the bundle root. Optional when the root contains exactly one DLL. |
| `ui` | UI directory relative to the bundle root. Defaults to `ui` when it contains `index.html`. |
| `uiEntry` | Entry HTML file relative to the UI directory. Defaults to `index.html`. |

Rules worth internalizing:

- The `id` is what users and SkyLoader agree on across renames, zips, and
  updates. Changing it creates a *second* plugin.
- Every path in the manifest stays inside the bundle. `../` escapes are rejected.
- A zip may carry the files at its root or inside one top-level folder; both
  import identically.
- A single bare DLL also imports: SkyLoader falls back to the DLL's
  `VERSIONINFO` resource (`SkyPluginId`, `PluginName`, `Version`, `Author`,
  `Description`), then to the filename, and picks up a neighboring `ui` or
  `<name>.ui` folder automatically. A manifest is still the best option because
  there is exactly one source of truth.

## 3. How a bundle becomes a managed plugin

Importing (drag-drop, or **Add plugins** in the launcher) runs a staged
workflow:

1. The input is unpacked into a `.stage-` directory next to the store.
2. SkyLoader discovers the manifest, resolves the DLL from it, and validates
   paths.
3. The identity is compared against the registry:
   - A higher version replaces the installed bundle.
   - An equal version is accepted, so re-importing a rebuilt artifact works.
   - A lower version is rejected unless the import is explicitly forced.
   - Metadata on only one side triggers a confirmation prompt.
4. The staged tree is promoted into the managed store:
   `%LOCALAPPDATA%\SkyLoader\plugins\<id>\`. A currently locked bundle is
   parked to `.old-<id>` and swapped after the game releases it.

After that the bundle is a **managed plugin**: it lives in the store, is
referenced by `config.json`, and its `ui/` files are served by the launcher's
UI server. Legacy DLLs registered by absolute path keep working, but they do
not get a managed directory.

## 4. Load and unload inside the game

The DLL itself is loaded only when Sky runs with the bootstrap host active:

- **Launch Sky from SkyLoader** with the plugin enabled → the launcher injects
  `SkyBootstrap.dll`, then asks the host to `LOAD` each enabled plugin. The
  host `LoadLibraryEx`es the DLL and calls `SkyPluginInit` on a bootstrap
  worker thread. Rejecting init (returning `0`) unloads the DLL again.
- **Toggling Enable/Disable while Sky runs** loads or unloads the plugin
  immediately. `SkyPluginShutdown` runs before unload.
- Closing Sky unloads everything implicitly; no in-session state survives.

That is also why plugin UI requests fail before a launch:
`"Sky is not running with SkyBootstrap yet. Launch Sky from SkyLoader, then try
again."` The UI bridge is only live while the host is in the game.

## 5. Where state may live

The samples keep all state in memory on purpose. Consider it the recommended
baseline: the host gives a plugin no filesystem API, and the bundle directory
is replaced wholesale on updates, so anything a plugin wrote into its own
bundle disappears with the next import.

When a plugin must remember something between sessions, it can:

- resolve its own DLL path (`GetModuleFileName` on the module handle) and keep
  data in a folder it creates nearby — accepting that an update deletes it; or
- treat the launcher-facing UI as the editor of ephemeral state, exactly like
  the three samples do.

Decide this consciously per plugin; do not drift into "sometimes it persists"
behavior.

## 6. Checklist before distributing

1. One DLL, one manifest, optional `ui/` — nothing else required.
2. `id` stable, unique, lowercase-friendly; matches any `VERSIONINFO` fallback.
3. `version` moves forward with every release users should get.
4. The DLL exports `SkyPluginInit` (required) plus the optional exports it
   uses ([`02-writing-the-plugin-dll.md`](02-writing-the-plugin-dll.md)).
5. The UI works from relative paths only
   ([`03-ui-bridge.md`](03-ui-bridge.md)).
6. Import into a real SkyLoader, launch Sky, run through the test checklist in
   [`05-testing-and-troubleshooting.md`](05-testing-and-troubleshooting.md).
