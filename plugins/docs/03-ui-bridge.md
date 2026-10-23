# Communicating with the UI: the bridge, `window.sky`, and messages

Plugin UIs are ordinary web pages, but everything that matters happens across
two channels SkyLoader provides. This page explains both, the guarantees and
limits on the way, and the page-side patterns the samples use.

## 1. The full path of one request

```text
 plugin page (launcher iframe, or a browser tab)
   │  window.sky.call({v:1, action:"get"})            window.sky.onEvent
   ▼                                                        ▲
 HTTP POST /api/<token>/<id>/rpc    body ≤ 2 MiB           SSE GET /api/<token>/<id>/events
   │  (loopback only; token gates every path)               │ launcher polls the pipe
   ▼  internal/uiserver — serves bundles, proxies calls     │ every 350 ms
 Launcher.PluginRPC → named pipe:  "UI <id> <base64>"       │ "POLL <id>" → per-plugin queue
   ▼                                                        ▲
 SkyBootstrap.dll inside Sky.exe — the host hands one        │
 message at a time to the plugin (serialized, ≤ 4 MiB)       │
   ▼                                                        │
 SkyPluginUiMessage(request_json, response_json, capacity)   │ api->post_event(json)
```

The bridge is only alive while Sky runs with the plugin loaded. Before that,
`window.sky.call` rejects with
`"Sky is not running with SkyBootstrap yet..."` — the page should surface that
state instead of spinning.

Both endpoints are on a random loopback port, and a random token is part of
every path (`/p/<token>/<id>/...`, `/api/<token>/<id>/...`). Anything process
local could have held those URLs; treat them as bearer credentials: they grant
full call access until SkyLoader restarts. Never log them, never hand them to
third-party code (the samples just use relative URLs, which inherit the token
(`<base href="/p/<token>/<id>/">` is injected into every HTML page)).

## 2. `window.sky` — the page API

Injected by SkyLoader into every served HTML page (do not ship your own bridge
script):

| Member | Behavior |
| --- | --- |
| `window.sky.plugin` | `{ id, name, version }`, straight from the manifest. |
| `window.sky.call(payload)` | Posts `payload` as JSON to the plugin's `SkyPluginUiMessage` and resolves with the parsed reply. Empty replies resolve to `null`; non-JSON replies are wrapped as `{"raw": "..."}`. Rejects (throws) on HTTP failures; the error carries `error.data`. |
| `window.sky.onEvent(listener)` | Subscribes to plugin-pushed events (see below); returns an unsubscribe function. |
| `window.sky.close()` | Asks the launcher to close the in-app plugin iframe. |
| `window.sky.openExternal(url)` | Opens an `http(s)` URL in the system browser; other schemes are rejected. |

One request = one reply. There are no streaming responses; pages that need
long-running work should reply quickly and push intermediate state with
events.

## 3. Events — the push channel

Plugins push from C++ through `api->post_event(api->event_context, json)`.
The host queues the event per plugin; the launcher drains the queue over SSE
every 350 ms; the bridge delivers each event as parsed JSON (or as a string
when the JSON was invalid) to every listener.

Guarantees and limits:

- FIFO per plugin, delivered at most once per session; **no replay** after a
  page reload or reconnect.
- Dropped when the plugin's queue exceeds its budget: 256 events, 1 MiB
  total, 64 KiB per event. Post small events; if you have to send a lot, push
  a `{"type":"...","dirty":true}` nudge and let the page `call` for details.
- `post_event` is safe from any thread, including the background thread in
  sample 03. It validates JSON on the way in and rejects oversized events, so
  a `false` return means "shrunk or serialized too late", not a crash.

Events and calls are complementary: a page subscribes once
(`window.sky.onEvent`) and reacts to refresh notifications, while every user
action goes through `call`. Use events for *state you own pushing* (sample
03's `tick`, or "a background scan finished"); do not poll with `call` on a
timer — that is just a slow, keep-alive-emitting event feed built backwards.

## 4. Response contract details

What your `SkyPluginUiMessage` can return, and what the page sees:

| Plugin returns | Page (`window.sky.call`) sees |
| --- | --- |
| valid JSON bytes + length ≥ 0 | parsed JSON value (objects recommended) |
| length 0 (empty) | `null` |
| negative return | throws `Error` with `error.data = {"error": "<message>"}` (HTTP 502) |
| text that is not valid JSON | `{"raw": "<text>"}` — still fine, but prefer JSON |
| bytes longer than 4 MiB | negative path; the host reports the response as too large |

Request payloads come through the launcher capped at **2 MiB**; requests
bigger than that fail before your code runs. Both numbers exist in the page
design space: 2 MiB per request, 4 MiB per reply buffer, 64 KiB per event.

## 5. Authoring the page

- Plain static files are enough and are what the samples ship. Bundlers
  (Vite/React/Svelte) also work when they emit prebuilt relative-URL bundles;
  SkyLoader never runs npm or a dev server.
- Keep asset URLs relative. The injected `<base>` pins them to the
  authenticated prefix; absolute `/assets/...` paths break inside the iframe.
- The page runs in SkyLoader's window (iframe) or a normal browser tab, from
  the same URLs. `window.sky.close()` only matters inside the launcher.
- `localStorage`/`sessionStorage` are web-only and keyed by the loopback
  origin, which changes every SkyLoader run — treat them as scratch space,
  not persistence (sample 01 stores everything in the plugin instead).
- Show the user the truth about each call: the samples print every request
  and reply into a wire log, which doubles as the best debugging tool in
  development.

## 6. Reference pattern from the samples

Every sample's `app.js` shares one helper. Keep it near-verbatim in your
projects:

```js
async function request(action, params = {}) {
  const payload = { v: 1, action, ...params };
  try {
    const reply = await window.sky.call(payload);
    if (!reply || reply.ok !== true) {
      // Application-level error that traveled inside the envelope.
      const details = reply && reply.error
        ? reply.error
        : { code: "BAD_REPLY", message: "Not a success envelope." };
      throw new Error(details.message || details.code);
    }
    return reply.data === undefined ? null : reply.data;
  } catch (error) {
    // Transport-level error (502, bridge not ready, plugin crash).
    showBanner("Request failed: " + error.message);
    throw error;
  }
}

window.sky.onEvent((event) => { /* any JSON value; ours always have "type" */ });
```

Note the deliberate split: failures *inside* the envelope give the page
context (`error.code`, `error.message`, `error.detail`) and render nicely;
failures *of transport* are unexpected and deserve a loud banner.

For the C++ side that pairs with it — envelope shapes, limit budgets, and how
to design payloads for fields, lists, and nested structs — see
[`04-data-patterns.md`](04-data-patterns.md).
