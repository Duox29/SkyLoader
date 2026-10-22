(() => {
  const config = window.__SKY_UI__ || {};
  const plugin = config.plugin || {};
  const listeners = new Set();
  let events = null;

  function connectEvents() {
    if (events || !config.eventsUrl) return;
    events = new EventSource(config.eventsUrl);
    events.onmessage = (message) => {
      let payload;
      try {
        payload = JSON.parse(message.data);
      } catch {
        payload = message.data;
      }
      for (const listener of listeners) {
        try {
          listener(payload);
        } catch (error) {
          console.error("[sky] event listener failed", error);
        }
      }
    };
  }

  async function call(payload) {
    if (!config.rpcUrl) throw new Error("SkyLoader UI bridge is not configured.");
    const response = await fetch(config.rpcUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(payload === undefined ? null : payload),
    });
    const text = await response.text();
    let data = null;
    try {
      data = text ? JSON.parse(text) : null;
    } catch {
      data = text;
    }
    if (!response.ok) {
      const message = data && data.error ? data.error : `Plugin request failed (${response.status})`;
      const error = new Error(message);
      error.data = data;
      throw error;
    }
    return data;
  }

  function onEvent(listener) {
    if (typeof listener !== "function") throw new TypeError("listener must be a function");
    listeners.add(listener);
    connectEvents();
    return () => listeners.delete(listener);
  }

  function close() {
    window.parent.postMessage({ type: "sky:close" }, "*");
  }

  function openExternal(url) {
    window.parent.postMessage({ type: "sky:open-external", url: String(url) }, "*");
  }

  window.sky = {
    plugin: { id: plugin.id || "", name: plugin.name || "", version: plugin.version || "" },
    call,
    onEvent,
    close,
    openExternal,
  };
})();
