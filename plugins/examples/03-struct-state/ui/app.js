// app.js -- Struct State page. Combines every pattern: renders a nested
// document, edits it through a patch, reads single leaves with dot paths, and
// listens to events the plugin pushes from a background thread.
"use strict";

const state = { doc: null };

function $(selector) {
  return document.querySelector(selector);
}

const wire = $("#wire");
let wireLines = 0;

function noteWire(prefix, text, bad) {
  const line = document.createElement("div");
  line.className = "line" + (bad ? " line-bad" : prefix === "←" ? " line-ok" : "");
  line.textContent = prefix + " " + text;
  wire.appendChild(line);
  wireLines += 1;
  while (wireLines > 60) {
    wire.removeChild(wire.firstChild);
    wireLines -= 1;
  }
  wire.scrollTop = wire.scrollHeight;
}

// One shared envelope for calls; events arrive separately with onEvent.
async function request(action, params = {}) {
  const payload = { v: 1, action, ...params };
  noteWire("→", JSON.stringify(payload));
  try {
    const reply = await window.sky.call(payload);
    noteWire("←", JSON.stringify(reply));
    if (!reply || reply.ok !== true) {
      const error = reply && reply.error
        ? reply.error
        : { code: "BAD_REPLY", message: "The reply was not a success envelope." };
      const failure = new Error(error.message || error.code);
      failure.code = error.code;
      throw failure;
    }
    return reply.data === undefined ? null : reply.data;
  } catch (error) {
    noteWire("✗", String(error.message || error), true);
    throw error;
  }
}

function showError(box, error) {
  if (typeof box === "string") box = $(box);
  box.textContent = (error.code ? error.code + ": " : "") + error.message;
  box.classList.remove("hidden");
}

function clearError(box) {
  if (typeof box === "string") box = $(box);
  box.classList.add("hidden");
}

// --- rendering ---------------------------------------------------------------

function render() {
  const doc = state.doc;
  if (!doc) return;
  $("#nickname").value = doc.profile?.nickname ?? "";
  $("#candles").value = String(doc.profile?.candles ?? 0);
  $("#realm").value = doc.profile?.realm ?? "Isle of Dawn";
  $("#autoCollect").checked = doc.settings?.autoCollect === true;

  const schedule = $("#schedule");
  schedule.textContent = "";
  const items = doc.schedule ?? [];
  $("#schedule-stats").textContent = "(" + items.length + " items)";
  if (items.length === 0) {
    const li = document.createElement("li");
    li.className = "empty";
    li.textContent = "Nothing scheduled; slot.0 waits.";
    schedule.appendChild(li);
  }
  items.forEach((item, index) => {
    const li = document.createElement("li");
    const position = document.createElement("span");
    position.className = "index";
    position.textContent = "schedule." + index;
    const text = document.createElement("span");
    text.className = "text";
    const when = document.createElement("span");
    when.className = "when";
    when.textContent = (item.when || "?") + " ";
    text.appendChild(when);
    text.appendChild(document.createTextNode(item.what || ""));
    const remove = document.createElement("button");
    remove.textContent = "×";
    remove.addEventListener("click", () =>
      send("schedule-remove", { index }).catch((error) =>
        showError("#error-road", error)));
    li.appendChild(position);
    li.appendChild(text);
    li.appendChild(remove);
    schedule.appendChild(li);
  });
}

async function send(action, params) {
  const data = await request(action, params);
  // get-path replies describe one leaf; every other action replies with the
  // authoritative document.
  if (action !== "get-path" && data && (data.meta || data.profile)) {
    state.doc = data;
    render();
  }
  return data;
}

// --- events ------------------------------------------------------------------

window.sky.onEvent((event) => {
  // Events are raw JSON values; ours always carry a "type" string.
  const type = event && typeof event === "object" ? event.type : "?";
  const status = $("#event-status");
  if (status.textContent !== "") status.textContent = "";
  const li = document.createElement("li");
  li.textContent = JSON.stringify(event);
  if (type === "ready") li.classList.add("live");
  const feed = $("#events");
  feed.prepend(li);
  while (feed.children.length > 20) feed.removeChild(feed.lastChild);
});

// --- actions -----------------------------------------------------------------

async function saveProfile() {
  // A patch is atomic: every dot path is validated before one value moves.
  const fields = {};
  const nickname = $("#nickname").value.trim();
  if (nickname) fields["profile.nickname"] = nickname;
  fields["profile.candles"] = Number.parseInt($("#candles").value, 10) || 0;
  fields["profile.realm"] = $("#realm").value;
  fields["settings.autoCollect"] = $("#autoCollect").checked;
  try {
    await send("patch", { fields });
  } catch (error) {
    showError("#error-road", error);
  }
}

async function loadDoc() {
  clearError("#error-road");
  try {
    state.doc = await request("get");
    render();
  } catch (error) {
    showError("#error-road", error);
  }
}

async function addSchedule() {
  clearError("#error-road");
  const when = $("#when").value.trim();
  const what = $("#what").value.trim();
  try {
    await send("schedule-add", { when, what });
    $("#when").value = "";
    $("#what").value = "";
  } catch (error) {
    showError("#error-road", error);
  }
}

async function pathRead() {
  const result = $("#path-result");
  try {
    const data = await send("get-path", { path: $("#path-input").value.trim() });
    result.textContent = "found: " + JSON.stringify(data.value);
  } catch (error) {
    result.textContent = "read failed: " + error.message;
  }
}

async function pathWrite() {
  const result = $("#path-result");
  let value;
  try {
    // The page parses the typed value; the plugin receives ready-made JSON.
    value = JSON.parse($("#path-value").value);
  } catch {
    result.textContent = "the value must be valid JSON (quoted strings too)";
    return;
  }
  try {
    const data = await send("set-path",
      { path: $("#path-input").value.trim(), value });
    result.textContent = "wrote: " + JSON.stringify(data.value);
  } catch (error) {
    result.textContent = "write failed: " + error.message;
  }
}

// --- boot --------------------------------------------------------------------

$("#plugin-meta").textContent =
  [window.sky.plugin.id, window.sky.plugin.version].filter(Boolean).join(" · ");
document.getElementById("save-profile").addEventListener("click", saveProfile);
document.getElementById("reload").addEventListener("click", loadDoc);
document.getElementById("add-schedule").addEventListener("click", addSchedule);
document.getElementById("path-get").addEventListener("click", pathRead);
document.getElementById("path-set").addEventListener("click", pathWrite);

loadDoc();
