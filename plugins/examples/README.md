# Sample plugins

Three complete SkyLoader plugins, one per data pattern. Each is a self-
contained bundle (manifest + DLL source + UI) that loads through the normal
import flow and speaks the real `window.sky` bridge.

| Sample | Teaches | Wire surface |
| --- | --- | --- |
| [`01-field-settings/`](01-field-settings/) | **field** — a flat document of scalars | `get`, `set` (batch, atomic, rule-table validated), `reset`; posts a `fields` event on change |
| [`02-list-collection/`](02-list-collection/) | **list** — an ordered collection with stats | `get`, `add`, `replace`, `remove`, `move`, `sort`, `clear`, `about`; replies always carry the authoritative array |
| [`03-struct-state/`](03-struct-state/) | **struct** — a nested document reached by dot paths | `get`, `get-path`, `set-path`, `patch`; `schedule-add`, `schedule-remove`; pushes `ready` and `tick` events from background threads |

Together they exercise both bridge channels — request/response (`call`) and
plugin-pushed events (`onEvent`) — with the limits and the error envelope
documented in [`../docs/04-data-patterns.md`](../docs/04-data-patterns.md).

## Building the DLLs

`common/skyplugin.h` is a kit both parsers and plumbing share; every sample
compiles from one `.cpp`.

| Toolchain | Command |
| --- | --- |
| build helper, Windows | build all: `build.cmd` → one: `build.cmd 01-field-settings` |
| build helper, cross | `./build.sh` (Zig preferred, or MinGW-w64) |
| MSVC | `cl /nologo /LD /EHsc /std:c++17 /O2 /I"..\..\libraries\SkyBootstrap\include" /I"..\common" field-settings.cpp /Fe:field-settings.dll` *(x64 prompt)* |
| gcc/clang cross | `g++ -std=c++17 -O2 -shared -static -I ../../../libraries/SkyBootstrap/include -I ../common field-settings.cpp -o field-settings.dll` |

Requirements: a 64-bit target, plus the paths above so `#include
"skyplugin.h"` finds the ABI header (`skybootstrap_api.h`, unchanged) and the
kit. The generated DLLs sit next to each `plugin.json`; mix and match.

## Installing and driving the samples

1. Import a sample folder into SkyLoader (drag-drop or **Add plugins**). The
   manifest carries the identity, so the launcher shows name, version, and
   the UI badge.
2. Enable it and **Launch Sky from SkyLoader** — DLLs load through the
   bootstrap host inside Sky.
3. Open the plugin's **UI** button in the launcher. Every panel maps to the
   concepts: a form (01/03), a list (02), a path explorer and event feed (03),
   and a wire log of the exact JSON crossing `window.sky`.

### What to try per sample

**01 Field Settings**
- Save values and read the wire log: request, then the full authoritative
  document in the reply.
- **Demo: invalid field** posts `volume: 999` and lands an `OUT_OF_RANGE`
  envelope with `detail.field` — proof errors ride inside the reply, not as
  thrown transport failures.
- Reset restores the defaults and (again) shows a full re-render.

**02 List Collection**
- Add three items, drag the list around (`↑`/`↓` = `move`), rename inline
  (`replace`), sort, clear — the list stays authoritative from the plugin.
- **Demo: bad index** issues `remove {index: 999}`; expect a `NOT_FOUND`
  envelope and no state change.

**03 Struct State**
- **Patch profile + settings** shows a multi-path atomic update; fill in
  `profile.candles` from a state the *plugin* owns (page data is only a prop).
- The **Path explorer** drives `get-path`/`set-path` on arbitrary dot paths —
  try `schedule.0`, then `schedule.9` to see the strictness rules.
- The **Events** feed lists `ready` (at load) and the `tick` ticker the
  plugin pushes every 5 s from a background thread, no polling.

## `common/skyplugin.h` in one screen

```cpp
skyplugin::init(api, "My plugin")  // adopt host; false → reject (host too old)
skyplugin::setHandler(&handle);    // one function per plugin, serialized calls
skyplugin::dispatch(req, resp, cap);  // bridging trampoline for the export

// inside the handler: read the request, then answer with an envelope
response = skyplugin::okReply(data);
response = skyplugin::errorReply("NOT_FOUND", "no such key");

skyplugin::postEvent(event);       // enqueue JSON for onEvent listeners
skyplugin::log("text");            // SkyBootstrap.log diagnostics

// data: JSON value with parsing, writing, and typed lookups
skyplugin::Value doc;
skyplugin::parse(text, &doc);
skyplugin::stringOr("key", "") / numberOr / boolOr / at / item / size
```

The samples are the spec for how to wire values into a real handler; the full
reference for the host side of the same exchange is
[`docs/creating-a-plugin.md`](../../docs/creating-a-plugin.md).
