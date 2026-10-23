// app.js -- List Collection page. Renders the plugin's authoritative array and
// sends small per-mutation actions. Note how every reply replaces the whole
// rendered list: no local diffing, no local ordering, no stale rows.
"use strict";

const state = { items: [], stats: { count: 0, totalLength: 0 } };

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

function showError(error) {
  const box = $("#error");
  box.textContent = (error.code ? error.code + ": " : "") + error.message;
  box.classList.remove("hidden");
}

function clearError() {
  $("#error").classList.add("hidden");
}

// --- rendering ---------------------------------------------------------------

function render() {
  const list = $("#items");
  list.textContent = "";
  if (state.items.length === 0) {
    const li = document.createElement("li");
    li.className = "empty";
    li.textContent = "Empty list — add something above.";
    list.appendChild(li);
  }
  state.items.forEach((text, index) => list.appendChild(row(text, index)));
  $("#stats").textContent =
    "(" + state.stats.count + " items, " + state.stats.totalLength + " bytes)";
}

function row(text, index) {
  const li = document.createElement("li");

  const position = document.createElement("span");
  position.className = "index";
  position.textContent = String(index);
  li.appendChild(position);

  const label = document.createElement("span");
  label.className = "text";
  label.textContent = text;
  li.appendChild(label);

  li.appendChild(rowButton("rename", () => startRename(li, index)));
  li.appendChild(rowButton("↑", () => send("move", { from: index, to: index - 1 })));
  li.appendChild(rowButton("↓", () => send("move", { from: index, to: index + 1 })));
  li.appendChild(rowButton("×", () => send("remove", { index })));
  return li;
}

function rowButton(label, onClick) {
  const button = document.createElement("button");
  button.textContent = label;
  button.addEventListener("click", onClick);
  return button;
}

function startRename(row, index) {
  const text = state.items[index];
  const input = document.createElement("input");
  input.type = "text";
  input.maxLength = 140;
  input.value = text;

  const save = () => send("replace", { index, item: input.value.trim() || text });
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter") save();
    if (event.key === "Escape") render();
  });
  input.addEventListener("blur", render);

  const label = row.querySelector(".text");
  row.replaceChild(input, label);
  input.focus();
  input.select();
}

// --- actions -----------------------------------------------------------------

// Guard the move buttons' natural indexes; the plugin validates too.
async function send(action, params) {
  clearError();
  const visible = state.stats.count;
  if (action === "move") {
    const target = params.to;
    if (target < 0 || (typeof target === "number" && target >= visible)) return;
  }
  try {
    applyState(await request(action, params));
  } catch (error) {
    showError(error);
  }
}

function applyState(data) {
  // The plugin is authoritative: adopt its array and stats verbatim.
  state.items = (data && data.items) || [];
  state.stats = (data && data.stats) || { count: 0, totalLength: 0 };
  render();
}

// --- boot --------------------------------------------------------------------

$("#plugin-meta").textContent =
  [window.sky.plugin.id, window.sky.plugin.version].filter(Boolean).join(" · ");

document.getElementById("add").addEventListener("click", () => {
  const input = document.getElementById("new-item");
  const text = input.value.trim();
  if (!text) return;
  input.value = "";
  send("add", { item: text });
});
document.getElementById("new-item").addEventListener("keydown", (event) => {
  if (event.key === "Enter") {
    document.getElementById("add").click();
  }
});
document.getElementById("sort-asc").addEventListener("click", () => send("sort", { order: "asc" }));
document.getElementById("sort-desc").addEventListener("click", () => send("sort", { order: "desc" }));
document.getElementById("sort-reverse").addEventListener("click", () => send("sort", { order: "reverse" }));
document.getElementById("clear").addEventListener("click", () => send("clear"));
// Out-of-range index on purpose, to watch the NOT_FOUND envelope flow back.
document.getElementById("demo-bad").addEventListener("click", () => send("remove", { index: 999 }));

request("get").then(applyState).catch(showError);
