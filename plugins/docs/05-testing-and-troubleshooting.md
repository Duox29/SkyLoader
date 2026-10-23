# Testing and troubleshooting plugin UIs

Development rhythm, logs, in-place debugging, and the failure table that saves
an afternoon. The upstream test checklist lives in
[`docs/creating-a-plugin.md`](../../docs/creating-a-plugin.md); this page adds
the UI-specific workflow from the samples.

## 1. The development loop

```text
edit .cpp          → build (build.cmd / build.sh / manual compile)
                   → Sky close? → import bundle (drag the folder or zip)
                   → enable plugin
                   → Launch Sky from SkyLoader
                   → open the plugin UI → exercise actions
                   → (close Sky) → repeat
```

Details that matter:

- A running Sky may hold the old DLL; Windows refuses to overwrite a loaded
  DLL, so close Sky before importing updates. SkyLoader parks locked bundles
  as `.old-<id>` and swaps them, but a clean loop is quicker.
- The import dialog checks version order: bump `version` in `plugin.json`
  before every re-import so the update is never rejected as "older".
- Enable/disable still works while Sky runs — great for reload testing when
  your code is DLL-free, but a rebuild always needs a fresh injection.

## 2. Logs

| Log | Path | Watch for |
| --- | --- | --- |
| Launcher | `%LOCALAPPDATA%\SkyLoader\logs\SkyLoader.log` | injection, UI server errors, import lifecycle |
| Bootstrap host | `%LOCALAPPDATA%\SkyLoader\logs\SkyBootstrap.log` | `Loaded plugin <path>`, your `api->log(...)` lines, unload |

Sample kits log `"Your name initialized (ABI 4)"` at init and one line per
shutdown. If init logs, the exports resolved and `SkyPluginInit` accepted the
host; everything after can't **not** appear in the log.

## 3. Debugging the UI half

- **Wire log** — every sample's page prints each request and reply with their
  JSON intact. 80% of "it doesn't work" questions are answered by reading the
  last line versus the expectation.
- **Open the page in a real browser** — the launcher's "Open in browser" (or
  a normal browser pointed at the same authenticated URL) gives you standard
  DevTools: network tab for `/rpc` bodies, EventSource frames for
  `/events`, and a real console for page errors. Same tokens, same behavior,
  honest stack traces. They die with the launcher session, like the iframe.
- **F12 inside the launcher's WebView2** depends on the host's DevTools
  support; don't rely on it, use the browser.
- **`/healthz`** (204 on the launcher's loopback port) is the fastest liveness
  check for the UI server.

## 4. Fault table

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| Import fails: identity says "must match" | manifest `id` vs `VERSIONINFO` disagree | keep one, drop the other |
| Import says "failed" or the bundle vanishes | zip layout, path escaping, cycle | re-read manifest section of [`01-plugin-anatomy.md`](01-plugin-anatomy.md) |
| `LoadLibrary failed` in SkyBootstrap.log | DLL is 32-bit, or dependencies are missing | x64 build; static runtime (`-static`/`/MT`) |
| `SkyPluginInit export is missing` | missing `dllimport`/export or wrong toolchain | extern "C" + one shared name; see doc 02 |
| Plugin enabled but `Loaded plugin` never appears | SkyLoader never fired the inject | launch via SkyLoader, check Auto-inject |
| The UI opens but `sky.call` rejects immediately | Sky is closed, or plugin disabled/not loaded | re-read the caller: launch first; verify Enable |
| HTTP 502 with `{"error": "..."}` | your handler returned `< 0` or failed a call | inspect SkyBootstrap.log; the "not valid JSON" path |
| Reply arrives but is `{"raw": "..."}` | handler text wasn't valid JSON | build replies with the kit's `write()` |
| Events missing | queue full (256/1 MiB budget); page didn't subscribe | smaller events; verify `onEvent` ran |
| UI blank or 404 | absolute asset URL, wrong `uiEntry`, or wrong case in the path | relative URLs only; check the manifest entry |
| Settings lost after restart | the plugin has no persistence; by design, expected | read [`01-plugin-anatomy.md`](01-plugin-anatomy.md) section 5 |
| Update gets rejected under "older"-check | lower `version` string | bump the manifest version |

## 5. Ready-to-run checks per phase

1. **Import** — name, version, author, description, and the **UI** badge all
   render in the launcher list.
2. **Launch** — SkyBootstrap.log gains an init line for your plugin.
3. **UI loads** — sample 01's page fills defaults from the plugin; sample 03
   shows the ready event within ~350 ms.
4. **Round trip** — exercise at least one read and one write action; replay
   both with the wire log open.
5. **Rollback safety** — send a deliberately invalid write (sample 01's
   "Demo: invalid field", sample 02's "Demo: bad index"), confirm the error
   envelope and that nothing else moved.
6. **Reload** — disable + enable while Sky runs; verify the page re-syncs
   from fresh defaults (`get` after re-init, not stale page state).
7. **Update** — import a newer build with the same `id`; one entry remains
   and the DLL's session state resets at the next launch.

## 6. Removing cleanly

Remove while Sky is closed so the DLL and the event queue drop without
oversight: **Remove plugin** in the launcher deletes the managed bundle and
its config entry. Nothing about plugin data is left inside SkyLoader except
the store folder that's now yours to keep or delete.
