# Data patterns: field, list, struct

Your `SkyPluginUiMessage` handler is free, but samples and docs converge on
one protocol so every plugin avoids relearning the same mistakes:

- **Requests** are objects: `{"v":1,"action":"<name>", ...params}`.
- **Replies** are objects: `{"v":1,"ok":true,"data":<any>}` or
  `{"v":1,"ok":false,"error":{"code":"...","message":"...", "detail":<any>}}`.
- Unknown actions answer `{"v":1,"ok":false,"error":{"code":"UNKNOWN_ACTION"...}}`
  rather than crashing or returning `-1`.
- Reply with the **authoritative state** after every mutation (get/set/reset
  semantics); the page re-renders from it instead of hand-diffing.

`v` is part of the envelope: without it, a future breaking change would need
detection rituals on both ends. Ignore `v` you don't recognize only if you
know your defaults are safe; better: whitelist it and answer
`{"error":{"code":"BAD_REQUEST"}}` otherwise.

| Error code | When | Visible in |
| --- | --- | --- |
| `BAD_REQUEST` | request was not JSON / missing pieces | kit's `dispatch`, page |
| `INVALID_PAYLOAD` | right action, wrong parameters | sample handlers |
| `INVALID_TYPE` | parameter is the wrong JSON type | samples 01/02/03 |
| `UNKNOWN_FIELD` | unknown field key in a `set` batch | sample 01 |
| `OUT_OF_RANGE` | type is right, value illegal | samples 01/02 |
| `NOT_FOUND` | index/path/address does not exist | samples 02/03 |
| `INVALID_PATH` | dot path is malformed or rejected | sample 03 |
| `INVALID_FIELD` | struct field validation failure | sample 03 |
| `LIST_FULL` | list hit its capacity cap | samples 02/03 |
| `UNKNOWN_ACTION` | unread `action` value | kit's `dispatch` |
| `PLUGIN_ERROR` | handler threw an exception | kit's `dispatch` |

You may add codes; keep them as ASCII nouns so they survive copy/paste.

## 1. Field pattern — the scalar document

A flat JSON object where every key is one scalar: string, number, boolean.
Sample 01 (`plugins/examples/01-field-settings/`) implements it as:

```json
{ "nickname": "Sky Runner", "theme": "dark", "volume": 45,
  "sensitivity": 1.0, "notifications": true }
```

```json
request  {"v":1,"action":"set","fields":{"theme":"light","volume":78}}
reply    {"v":1,"ok":true,"data":{<the whole document again>}}
```

Rules worth copying:

1. **Validate first, mutate second.** Two passes → a `set` is atomic and a
   bad batch leaves nothing half-applied. Reasons detail the *field name* in
   `error.detail.field`.
2. **Rule table** (see sample 01's `FieldRule`): the number of fields grows,
   the handler code stays the same length; "try-catch the darkest case" beats
   copy-pasted `if` fields.
3. **Numbers are doubles.** Stay inside `[−2^53, 2^53]`; for counts, treat
   `int`-looking numbers as whole numbers and reject `1.5` for volume.
4. **`reset` matters.** An in-memory-only plugin restarts empty every
   session; a reset action saves the user from relaunching.
5. **Events announce identity, not data.** Sample 01 posts
   `{"type":"fields","keys":["volume","theme"]}` after mutation. The page may
   ignore it; a multi-window listener stays in sync cheaply.

| UI input | JSON type | C++ (kit) fit |
| --- | --- | --- |
| text input | string | `request.at(key).isString()`, `.string` |
| number/range/slider | number | `.isNumber()`, `.number` (double) |
| checkbox/switch | boolean | `.isBoolean()`, `.boolean` |
| select/dropdown | string enum | validate against a `const char*` list |

## 2. List pattern — the ordered collection

An ordered array whose item type is a shape (here: strings). Sample 02
(`plugins/examples/02-list-collection/`) carries the whole list in:

```json
request  {"v":1,"action":"add","item":"herald rune"}
reply    {"v":1,"ok":true,"data":{"items":["ember bell","herald rune"],
                                  "stats":{"count":2,"totalLength":22}}}
```

Actions in the sample: `get`, `add`, `replace`, `remove`, `move`, `sort`
(`asc|desc|reverse`), `clear`, plus `about` for the capacity contract.

Rules:

1. **Positional identity, and say so.** Replies are index-only. Removing
   element 0 shifts the rest, so the reply *always* carries the full array;
   a stable "id" would put optimistic-update pressure on both ends.
2. **Index*all* bounds**. `NOT_FOUND` beats out-of-range `operator[]` every
   time; encode `move`'s `from`/`to` as exactly two checks.
3. **Capacity is a contract** (see `about`): 200 items, 140 bytes per item in
   the sample. Distant pages can render badges off `stats` without the items.
4. **Sorting is server-side**: one switch, deterministic, and reply restores
   order for all listeners.
5. Size guard: a big list is fine until each `reply` also includes it. When
   items would grow into hundreds of KiB, switch to summaries + explicit
   `get-page`-style actions (see "Sizing" below) instead of letting the
   transport decide.

## 3. Struct pattern — the nested document

Objects that contain objects, arrays, and scalars; leaves addressed by dot
path. Sample 03 (`plugins/examples/03-struct-state/`):

```json
{ "meta":     { "schemaVersion": 1 },
  "profile":  { "nickname": "Sky Runner", "candles": 120, "realm": "Isle of Dawn" },
  "settings": { "theme": "dark", "autoCollect": true },
  "schedule": [ { "when": "08:30", "what": "Candle run" } ] }
```

Actions: `get`, `get-path`, `set-path`, `patch`, `schedule-add`,
`schedule-remove`.

| Dot path | Resolves to |
| --- | --- |
| `profile.candles` | the number inside `profile` |
| `schedule.0` | the first schedule item (array index on `schedule`) |
| `profile.missing.x` | fails: an intermediate key is missing |
| `schedule.5` | write fails: arrays never grow through `set-path` |

Rules:

1. **Walk rules**: object components must exist to traverse; array components
   must be numbers that index existing items. Only the final component of an
   *object* may introduce a new key. Array growth goes through named actions
   (`schedule-add`) that own their shape.
2. **Atomic `patch`**: validate every path first, then apply all (the sample
   does two passes under one mutex). A rejected patch mutates nothing —
   asserted by its integration tests.
3. **Volume control**: `get` returns everything, including arrays-of-structs;
   `get-path` reads leaves cheaply. The page shows both: profile edits go
   through `patch`, the explorer uses `get-path`/`set-path`.
4. **Push events**: sample 03 posts `{"type":"tick","seq":n,"uptime":s}` from
   a background thread. The document stays on the same mutex only for the
   thread that touches it. The page never asks for these events.
5. **Schema versioning**: `meta.schemaVersion` lives *inside* the payload.
   Bump it when a structure goes away, gains a requirement, or invalidates
   caches. Unknown keys are also allowed to persist, which keeps forward and
   backward compatibility cheap.

## 4. Sizing and chunking

The transport is generous but finite: 2 MiB per request (HTTP), 4 MiB per
reply buffer (ABI), 64 KiB per pushed event, 256 events / 1 MiB per plugin
queue. Stay far below them:

- Get leaves (`get-path`) instead of re-reading whole docs per interaction.
- Text fields and item counts are naturally small; you never generate payloads
  of more than a few KiB by accident.
- For huge arrays (imports, logs), hand out slices with paging params
  (`get-page {from, count}` → `{page, items, hasNext}`) instead of oversizing
  one envelope.
- Prefer one bigger `call` over many tiny ones — a batched `patch` beats
  eighteen `set-path`s; latency and context switches vanish.

## 5. Picking a pattern

| Data | Pattern | Sample |
| --- | --- | --- |
| Settings with a few scalars | field | 01-field-settings |
| Ordered rows the user edits | list | 02-list-collection |
| Real domain model with depth, mixed types, background edits | struct (+ dot paths, patch, events) | 03-struct-state |

They compose: a struct document can contain lists (sample 03's `schedule`),
and a list can hold scalar-field-shaped items. Use the smallest pattern that
fits; grow upward, never downward.
