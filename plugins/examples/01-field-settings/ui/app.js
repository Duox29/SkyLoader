// app.js -- Field Settings page. Shows the request/response half of the UI
// bridge: every button sends one JSON envelope via window.sky.call and
// reconciles the inputs from the plugin's reply, which always carries the full
// authoritative field document.
"use strict";

const elements = {
  form: { nickname: $("#nickname"), theme: $("#theme"), volume: $("#volume"),
          sensitivity: $("#sensitivity"), notifications: $("#notifications") },
  volumeValue: $("#volume-value"),
};

function $(selector) {
  return document.querySelector(selector);
}

// --- bridge plumbing ---------------------------------------------------------

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

// --- form state --------------------------------------------------------------

function renderFields(fields) {
  // The plugin replies with the authoritative document; re-render all inputs
  // from it instead of trying to diff by hand.
  elements.form.nickname.value = fields.nickname ?? "";
  elements.form.theme.value = fields.theme ?? "auto";
  elements.form.volume.value = String(volumeOf(fields.volume));
  elements.form.sensitivity.value = String(numberOf(fields.sensitivity));
  elements.form.notifications.checked = fields.notifications === true;
  elements.volumeValue.textContent =
    "(" + volumeOf(fields.volume) + "/100)";
}

function numberOf(value) {
  return typeof value === "number" ? value : 0;
}

function volumeOf(value) {
  return Math.round(numberOf(value));
}

function readChanges() {
  const fields = {};
  const nickname = elements.form.nickname.value.trim();
  if (nickname) fields.nickname = nickname;
  fields.theme = elements.form.theme.value;
  fields.volume = volumeOf(elements.form.volume.value);
  fields.sensitivity = numberOf(parseFloat(elements.form.sensitivity.value)) || 0.25;
  fields.notifications = elements.form.notifications.checked;
  return fields;
}

// --- actions -----------------------------------------------------------------

async function reloadFields() {
  clearError();
  try {
    renderFields(await request("get"));
  } catch (error) {
    showError(error);
  }
}

async function saveChanges() {
  clearError();
  try {
    renderFields(await request("set", { fields: readChanges() }));
  } catch (error) {
    showError(error);
  }
}

async function resetFields() {
  clearError();
  try {
    renderFields(await request("reset"));
  } catch (error) {
    showError(error);
  }
}

// Sends volume 999 on purpose so the sample's error envelope can be observed.
async function demoInvalidField() {
  clearError();
  try {
    await request("set", { fields: { volume: 999 } });
  } catch (error) {
    showError(error);
  }
}

// --- boot --------------------------------------------------------------------

$("#plugin-meta").textContent =
  [window.sky.plugin.id, window.sky.plugin.version].filter(Boolean).join(" · ");
$("#volume").addEventListener("input", () => {
  elements.volumeValue.textContent = "(" + elements.form.volume.value + "/100)";
});
document.getElementById("reload").addEventListener("click", reloadFields);
document.getElementById("save").addEventListener("click", saveChanges);
document.getElementById("reset").addEventListener("click", resetFields);
document.getElementById("demo-bad").addEventListener("click", demoInvalidField);

reloadFields();
