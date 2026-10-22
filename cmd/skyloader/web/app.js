(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const api = () => (window.go && window.go.main && window.go.main.App) || null;

  let state = null;
  let currentPlugin = null;
  let updateDismissed = false;
  let toastCounter = 0;

  function invoke(name, ...args) {
    const app = api();
    if (!app || typeof app[name] !== "function") {
      return Promise.reject(new Error("SkyLoader bridge is not ready."));
    }
    return app[name](...args);
  }

  function errorText(error) {
    if (!error) return "Unknown error";
    if (typeof error === "string") return error;
    if (typeof error.message === "string") return error.message;
    return String(error);
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined && text !== null) node.textContent = text;
    return node;
  }

  function toast(kind, message) {
    if (!message) return;
    const container = $("toasts");
    const node = element("div", `toast ${kind || "info"}`, message);
    node.dataset.id = String(++toastCounter);
    container.appendChild(node);
    while (container.children.length > 4) container.firstElementChild.remove();
    setTimeout(() => {
      node.classList.add("leaving");
      setTimeout(() => node.remove(), 220);
    }, 4300);
  }

  function setBusy(button, busy, label) {
    if (!button) return;
    if (busy) {
      if (!button.dataset.idleLabel) button.dataset.idleLabel = button.textContent;
      button.textContent = label || "Working…";
      button.disabled = true;
    } else {
      if (button.dataset.idleLabel) button.textContent = button.dataset.idleLabel;
      button.disabled = false;
    }
  }

  async function refresh() {
    try {
      const snapshot = await invoke("GetState");
      if (snapshot) applyState(snapshot);
    } catch (error) {
      toast("error", errorText(error));
    }
  }

  function applyState(snapshot) {
    state = snapshot;
    $("version").textContent = snapshot.version ? `v${snapshot.version}` : "";
    renderPlugins();
    renderGame();
    renderUpdate();
    if (currentPlugin) {
      const updated = (snapshot.plugins || []).find((plugin) => plugin.id === currentPlugin.id);
      if (!updated || !updated.hasUI) {
        closePluginUI();
      } else {
        currentPlugin = updated;
        $("uiName").textContent = updated.name || updated.id;
        $("uiMeta").textContent = [updated.version, updated.author].filter(Boolean).join(" · ");
      }
    }
  }

  function renderPlugins() {
    const list = $("pluginList");
    const plugins = (state && state.plugins) || [];
    list.replaceChildren();
    const empty = plugins.length === 0;
    $("emptyState").classList.toggle("hidden", !empty);
    list.classList.toggle("hidden", empty);

    const enabled = plugins.filter((plugin) => plugin.enabled).length;
    const loaded = plugins.filter((plugin) => plugin.loaded).length;
    let summary = "No plugins installed.";
    if (!empty) {
      summary = `${enabled} of ${plugins.length} enabled`;
      if (loaded > 0) summary += ` · ${loaded} loaded in Sky`;
      if (plugins.some((plugin) => plugin.missing)) summary += " · missing files detected";
    }
    $("pluginSummary").textContent = summary;

    for (const plugin of plugins) {
      list.appendChild(pluginRow(plugin));
    }
  }

  function pluginRow(plugin) {
    const row = element("div", "plugin-row");
    if (!plugin.enabled) row.classList.add("disabled");
    if (plugin.missing) row.classList.add("missing");

    const check = element("label", "check");
    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.checked = Boolean(plugin.enabled);
    checkbox.disabled = Boolean(plugin.missing);
    checkbox.title = plugin.enabled ? "Disable plugin" : "Enable plugin";
    checkbox.addEventListener("change", async () => {
      checkbox.disabled = true;
      try {
        await invoke("SetPluginEnabled", plugin.id, checkbox.checked);
        await refresh();
      } catch (error) {
        checkbox.checked = !checkbox.checked;
        toast("error", errorText(error));
      } finally {
        checkbox.disabled = Boolean(plugin.missing);
      }
    });
    check.append(checkbox, element("span"));

    const info = element("div", "plugin-info");
    info.appendChild(element("strong", "plugin-name", plugin.name || plugin.id));
    if (plugin.version) info.appendChild(element("span", "plugin-version", `v${plugin.version}`));
    if (plugin.hasUI) info.appendChild(element("span", "badge ui", "UI"));
    if (plugin.loaded) info.appendChild(element("span", "badge loaded", "Loaded"));
    if (plugin.missing) info.appendChild(element("span", "badge missing", "Missing"));
    if (plugin.description) {
      info.appendChild(element("span", "plugin-sep", "·"));
      info.appendChild(element("span", "plugin-desc", plugin.description));
    }
    if (plugin.author) {
      info.appendChild(element("span", "plugin-sep", "·"));
      info.appendChild(element("span", "plugin-author", `by ${plugin.author}`));
    }
    info.title = [plugin.description, plugin.path].filter(Boolean).join("\n");

    const actions = element("div", "plugin-actions");
    if (plugin.hasUI) {
      const open = element("button", "btn small", "Open");
      open.type = "button";
      open.addEventListener("click", () => openPluginUI(plugin));
      actions.appendChild(open);
    }
    const folder = element("button", "btn ghost small", "Folder");
    folder.type = "button";
    folder.title = "Open plugin folder";
    folder.addEventListener("click", async () => {
      try {
        await invoke("OpenPluginFolder", plugin.id);
      } catch (error) {
        toast("error", errorText(error));
      }
    });
    actions.appendChild(folder);

    const remove = element("button", "btn danger small", "Remove");
    remove.type = "button";
    remove.addEventListener("click", () => removePlugin(plugin));
    actions.appendChild(remove);

    row.append(check, info, actions);
    return row;
  }

  async function removePlugin(plugin) {
    const managed = plugin.managed
      ? "The managed plugin folder will be deleted."
      : "The plugin will be unregistered. Its original file is left in place.";
    const running = state && state.gameRunning;
    const warning = running ? "\n\nClose Sky first if the DLL is currently loaded." : "";
    const confirmed = await confirmDialog(
      "Remove plugin?",
      `"${plugin.name || plugin.id}" will be removed from SkyLoader.\n\n${managed}${warning}`,
      "Remove",
      true
    );
    if (!confirmed) return;
    try {
      await invoke("RemovePlugin", plugin.id);
      toast("success", "Plugin removed.");
    } catch (error) {
      toast("error", errorText(error));
    }
    await refresh();
  }

  function renderGame() {
    if (!state) return;
    const dot = $("statusDot");
    dot.className = "dot";
    let status = "Sky is not running";
    if (state.gameRunning) {
      status = `Sky is running (PID ${state.gamePID || "?"})`;
      if (state.injected) {
        status += " · plugins loaded";
        dot.classList.add("running");
      } else {
        status += " · bootstrap not loaded";
        dot.classList.add("warn");
      }
    }
    $("gameStatus").textContent = status;
    $("statusText").textContent = state.status || "Ready.";
    $("launchBtn").textContent = state.gameRunning ? "Launch / Inject" : "Launch Sky";
    $("injectBtn").classList.toggle("hidden", !state.gameRunning || state.injected);

    const gamePath = $("gamePath");
    if (document.activeElement !== gamePath) gamePath.value = state.gamePath || "";
    const bootstrapPath = $("bootstrapPath");
    if (document.activeElement !== bootstrapPath) bootstrapPath.value = state.bootstrapPath || "";
    $("bootstrapState").textContent = state.bootstrapValid ? "SkyBootstrap.dll ready" : "SkyBootstrap.dll missing";
    $("bootstrapState").classList.toggle("missing", !state.bootstrapValid);
    $("autoInject").checked = Boolean(state.autoInject);
  }

  function renderUpdate() {
    if (!state) return;
    const update = state.update || {};
    $("updateBtn").disabled = Boolean(update.checking);
    $("updateBtn").textContent = update.checking ? "Checking…" : "Check updates";
    const visible = Boolean(update.available) && !updateDismissed;
    $("updateBanner").classList.toggle("hidden", !visible);
    if (!visible) return;
    $("updateTitle").textContent = `SkyLoader ${update.latestVersion || "update"} is available`;
    $("updateText").textContent = update.downloadedPath
      ? "The installer has been downloaded and is ready to run."
      : update.message || "Open the release page to download it.";
    $("updateAction").textContent = update.downloadedPath ? "Run installer" : "Open release";
  }

  async function openPluginUI(plugin) {
    try {
      const url = await invoke("OpenPluginUI", plugin.id);
      currentPlugin = plugin;
      $("uiName").textContent = plugin.name || plugin.id;
      $("uiMeta").textContent = [plugin.version, plugin.author].filter(Boolean).join(" · ");
      const frame = $("pluginFrame");
      frame.dataset.base = url;
      frame.src = url;
      $("uiLayer").classList.remove("hidden");
    } catch (error) {
      toast("error", errorText(error));
    }
  }

  function closePluginUI() {
    currentPlugin = null;
    const frame = $("pluginFrame");
    frame.removeAttribute("src");
    frame.dataset.base = "";
    $("uiLayer").classList.add("hidden");
  }

  function reloadPluginUI() {
    const frame = $("pluginFrame");
    const base = frame.dataset.base;
    if (!base) return;
    frame.src = `${base}${base.includes("?") ? "&" : "?"}_=${Date.now()}`;
  }

  async function handleImport(report) {
    if (!report) return;
    if (report.needsConfirm) {
      const conflicts = (report.items || []).filter((item) => item.action === "conflict");
      const names = conflicts
        .map((item) => item.name || item.id || item.source)
        .join("\n");
      const confirmed = await confirmDialog(
        "Replace existing plugins?",
        `SkyLoader found ${conflicts.length} conflicting plugin(s):\n\n${names}\n\nReplace the installed versions with the dropped files?`,
        "Replace",
        false
      );
      if (!confirmed) return;
      try {
        const retry = await invoke("ImportPaths", report.paths, true);
        if (retry.needsConfirm) {
          toast("error", "The plugin still conflicts after replacement.");
        } else if (retry.message) {
          toast("info", retry.message);
        }
      } catch (error) {
        toast("error", errorText(error));
      }
      await refresh();
      return;
    }
    await refresh();
  }

  function confirmDialog(title, message, confirmLabel, danger) {
    return new Promise((resolve) => {
      const modal = $("modal");
      const cancel = $("modalCancel");
      const confirm = $("modalConfirm");
      $("modalTitle").textContent = title;
      $("modalMessage").textContent = message;
      confirm.textContent = confirmLabel || "Continue";
      confirm.classList.toggle("danger", Boolean(danger));
      confirm.classList.toggle("primary", !danger);
      modal.classList.remove("hidden");

      const finish = (value) => {
        modal.classList.add("hidden");
        cancel.removeEventListener("click", onCancel);
        confirm.removeEventListener("click", onConfirm);
        modal.removeEventListener("click", onBackdrop);
        resolve(value);
      };
      const onCancel = () => finish(false);
      const onConfirm = () => finish(true);
      const onBackdrop = (event) => {
        if (event.target === modal) finish(false);
      };
      cancel.addEventListener("click", onCancel);
      confirm.addEventListener("click", onConfirm);
      modal.addEventListener("click", onBackdrop);
    });
  }

  function bind() {
    $("addBtn").addEventListener("click", async () => {
      const button = $("addBtn");
      setBusy(button, true, "Adding…");
      try {
        const report = await invoke("AddPlugins");
        await handleImport(report);
      } catch (error) {
        toast("error", errorText(error));
      } finally {
        setBusy(button, false);
      }
    });

    $("browseBtn").addEventListener("click", async () => {
      try {
        const path = await invoke("BrowseGame");
        if (path) toast("success", "Sky executable updated.");
        await refresh();
      } catch (error) {
        toast("error", errorText(error));
      }
    });

    $("browseBootstrapBtn").addEventListener("click", async () => {
      try {
        const path = await invoke("BrowseBootstrap");
        if (path) toast("success", "SkyBootstrap.dll updated.");
        await refresh();
      } catch (error) {
        toast("error", errorText(error));
      }
    });

    const commitGamePath = async () => {
      const value = $("gamePath").value.trim();
      if (!state || value === state.gamePath) return;
      try {
        await invoke("SetGamePath", value);
        toast("success", "Sky executable path saved.");
      } catch (error) {
        toast("error", errorText(error));
      }
      await refresh();
    };
    $("gamePath").addEventListener("change", commitGamePath);
    $("gamePath").addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        $("gamePath").blur();
      }
    });

    const commitBootstrapPath = async () => {
      const value = $("bootstrapPath").value.trim();
      if (!state || value === state.bootstrapPath) return;
      try {
        await invoke("SetBootstrapPath", value);
        toast("success", "Bootstrap path saved.");
      } catch (error) {
        toast("error", errorText(error));
      }
      await refresh();
    };
    $("bootstrapPath").addEventListener("change", commitBootstrapPath);
    $("bootstrapPath").addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        $("bootstrapPath").blur();
      }
    });

    $("autoInject").addEventListener("change", async () => {
      try {
        await invoke("SetAutoInject", $("autoInject").checked);
      } catch (error) {
        toast("error", errorText(error));
        await refresh();
      }
    });

    $("launchBtn").addEventListener("click", async () => {
      const button = $("launchBtn");
      setBusy(button, true, "Starting…");
      try {
        const message = await invoke("Launch");
        if (message) toast("info", message);
      } catch (error) {
        toast("error", errorText(error));
      } finally {
        setBusy(button, false);
        await refresh();
      }
    });

    $("injectBtn").addEventListener("click", async () => {
      const button = $("injectBtn");
      setBusy(button, true, "Injecting…");
      try {
        await invoke("Inject");
        toast("info", "Injecting SkyBootstrap into Sky…");
      } catch (error) {
        toast("error", errorText(error));
      } finally {
        setBusy(button, false);
      }
    });

    $("updateBtn").addEventListener("click", async () => {
      try {
        const update = await invoke("CheckUpdates");
        if (update && update.message) toast(update.available ? "success" : "info", update.message);
      } catch (error) {
        toast("error", errorText(error));
      }
      await refresh();
    });

    $("updateAction").addEventListener("click", async () => {
      try {
        await invoke("OpenUpdate");
      } catch (error) {
        toast("error", errorText(error));
      }
    });
    $("updateDismiss").addEventListener("click", () => {
      updateDismissed = true;
      renderUpdate();
    });

    $("folderBtn").addEventListener("click", () => invoke("OpenPluginsFolder").catch((error) => toast("error", errorText(error))));
    $("openPluginsFolder").addEventListener("click", () => invoke("OpenPluginsFolder").catch((error) => toast("error", errorText(error))));
    $("openAppFolder").addEventListener("click", () => invoke("OpenAppFolder").catch((error) => toast("error", errorText(error))));
    $("openLogs").addEventListener("click", () => invoke("OpenLogs").catch((error) => toast("error", errorText(error))));

    $("uiClose").addEventListener("click", closePluginUI);
    $("uiReload").addEventListener("click", reloadPluginUI);
    $("uiBrowser").addEventListener("click", () => {
      if (!currentPlugin) return;
      invoke("OpenPluginInBrowser", currentPlugin.id).catch((error) => toast("error", errorText(error)));
    });

    window.addEventListener("message", (event) => {
      const frame = $("pluginFrame");
      if (!currentPlugin || event.source !== frame.contentWindow) return;
      const data = event.data || {};
      if (data.type === "sky:close") {
        closePluginUI();
      } else if (data.type === "sky:open-external" && typeof data.url === "string") {
        if (/^https?:\/\//i.test(data.url)) {
          invoke("OpenExternal", data.url).catch((error) => toast("error", errorText(error)));
        } else {
          toast("warning", "Only http and https links can be opened.");
        }
      }
    });

    document.addEventListener("keydown", (event) => {
      if (event.key !== "Escape") return;
      if (!$("modal").classList.contains("hidden")) {
        $("modalCancel").click();
      } else if (!$("uiLayer").classList.contains("hidden")) {
        closePluginUI();
      }
    });

    let dragDepth = 0;
    const overlay = $("dropOverlay");
    const hasFiles = (event) => Array.from(event.dataTransfer?.types || []).includes("Files");
    window.addEventListener("dragenter", (event) => {
      if (!hasFiles(event)) return;
      dragDepth += 1;
      overlay.classList.remove("hidden");
    });
    window.addEventListener("dragover", (event) => {
      if (hasFiles(event)) event.preventDefault();
    });
    window.addEventListener("dragleave", () => {
      dragDepth = Math.max(0, dragDepth - 1);
      if (dragDepth === 0) overlay.classList.add("hidden");
    });
    window.addEventListener("drop", () => {
      dragDepth = 0;
      overlay.classList.add("hidden");
    });
  }

  function connectEvents() {
    if (!window.runtime || typeof window.runtime.EventsOn !== "function") return;
    window.runtime.EventsOn("state", (snapshot) => {
      if (snapshot) applyState(snapshot);
    });
    window.runtime.EventsOn("notice", (notice) => {
      if (notice && notice.message) toast(notice.kind || "info", notice.message);
    });
    window.runtime.EventsOn("import", (report) => {
      handleImport(report);
    });
  }

  async function waitForBridge() {
    for (let attempt = 0; attempt < 40; attempt += 1) {
      if (api()) return true;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    return false;
  }

  async function init() {
    bind();
    connectEvents();
    const ready = await waitForBridge();
    if (!ready) {
      toast("error", "SkyLoader bridge did not initialize. Restart the launcher.");
      return;
    }
    await refresh();
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init, { once: true });
  } else {
    init();
  }
})();
