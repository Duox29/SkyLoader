# SkyLoader plugins

Sample plugin bundles, the header kit they share, and step-by-step guides for
writing and debugging plugins. The canonical ABI contract lives in
[`docs/creating-a-plugin.md`](../docs/creating-a-plugin.md); these pages teach
the glue around it, with every claim tied to runnable code.

## Layout

```text
plugins/
├── docs/                                      guides (read in order)
│   ├── 01-plugin-anatomy.md                   bundle, manifest, managed store
│   ├── 02-writing-the-plugin-dll.md           exports, ABI, threading, building
│   ├── 03-ui-bridge.md                        window.sky, sizes, events, page design
│   ├── 04-data-patterns.md                    field / list / struct payload patterns
│   └── 05-testing-and-troubleshooting.md      dev loop, logs, fault table
└── examples/
    ├── common/skyplugin.h                     tiny kit: JSON value, envelope, host plumbing
    ├── 01-field-settings/                     scalar fields (get/set/reset rule table)
    ├── 02-list-collection/                    ordered list (add/replace/remove/move/sort)
    ├── 03-struct-state/                       nested document + dot paths + pushed events
    ├── build.cmd                              Windows build helper (MSVC/MinGW/Zig)
    └── build.sh                               cross build helper (Zig/MinGW)
```

## Reading path

1. [`docs/01-plugin-anatomy.md`](docs/01-plugin-anatomy.md) — what a plugin
   bundle is, how the identity/manifest/import cycle works.
2. [`docs/02-writing-the-plugin-dll.md`](docs/02-writing-the-plugin-dll.md) —
   what the host calls and when; minimal raw plugin, then the kit-shaped one.
3. [`docs/03-ui-bridge.md`](docs/03-ui-bridge.md) — the two channels (`call`
   and `onEvent`), limits, error contract, page patterns.
4. [`docs/04-data-patterns.md`](docs/04-data-patterns.md) — concrete payload
   designs for field, list, and struct data.
5. [`docs/05-testing-and-troubleshooting.md`](docs/05-testing-and-troubleshooting.md) —
   how to prove the plugin works inside SkyLoader.

## The samples

| Sample | Data pattern | Demonstrates |
| --- | --- | --- |
| `01-field-settings` | field | rule-table validation, atomic batch `set`, error envelopes, reset |
| `02-list-collection` | list | positional add/replace/move/sort/clear, capacity, `about` |
| `03-struct-state` | struct | nested doc, dot paths, atomic `patch`, background push events (`ready`, `tick`) |

Every sample is a complete SkyLoader plugin: manifest, DLL source, and a
prebuilt-ready UI that runs against the real bridge. They speak
`window.sky.call` / `window.sky.onEvent` end to end — the same contract any
plugin page has.

## Quick start

```sh
# Cross-build all sample DLLs (Zig is what SkyLoader itself uses)
cd plugins/examples && ./build.sh
```

Then either drag each sample folder onto the SkyLoader window, or open
SkyLoader's **Add plugins** and select a folder. Enable the plugin, launch Sky
from SkyLoader, and open the plugin's UI from the list.

More detail per sample — including what to click and which wire behavior to
watch — is in [`examples/README.md`](examples/README.md).
