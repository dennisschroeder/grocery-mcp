const HOST_NAME = "com.dennisschroeder.grocery_mcp";
const REWE_ORIGINS = ["https://www.rewe.de/*"];
const CONTENT_SCRIPT_RESPONSE_TIMEOUT_MS = 30_000;
const MUTATING_OPERATIONS = new Set(["basket_apply", "timeslot_reserve"]);
const RECONNECT_ALARM = "native-host-reconnect";
const RECONNECT_DELAY_MS = 1_000;
const RECONNECT_WATCHDOG_MINUTES = 0.5;

async function setBadge(text, color) {
  await chrome.action.setBadgeBackgroundColor({ color });
  await chrome.action.setBadgeText({ text });
}

const SAFE_ERROR_CODES = new Set([
  "permission_denied",
  "shop_reload_failed",
  "native_host_error",
  "content_script_unreachable",
  "operation_timeout",
]);

function safeErrorCode(error) {
  return SAFE_ERROR_CODES.has(error?.message) ? error.message : "unexpected_error";
}

async function setErrorBadge(code) {
  await setBadge("ERR", "#b3261e");
  await chrome.action.setTitle({ title: `grocery-mcp: ${code}` });
}

async function waitForNavigation(tabId, start, requireLoading) {
  await new Promise((resolve, reject) => {
    let timeout;
    let settled = false;
    let sawLoading = !requireLoading;
    const cleanup = () => {
      clearTimeout(timeout);
      chrome.tabs.onUpdated.removeListener(onUpdated);
      chrome.tabs.onRemoved.removeListener(onRemoved);
    };
    const complete = () => {
      if (settled) return;
      settled = true;
      cleanup();
      resolve();
    };
    const fail = () => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(new Error("shop_reload_failed"));
    };
    const onUpdated = (updatedTabId, changeInfo) => {
      if (updatedTabId !== tabId) return;
      if (changeInfo.status === "loading") sawLoading = true;
      if (changeInfo.status === "complete" && sawLoading) complete();
    };
    const onRemoved = (removedTabId) => {
      if (removedTabId === tabId) fail();
    };

    chrome.tabs.onUpdated.addListener(onUpdated);
    chrome.tabs.onRemoved.addListener(onRemoved);
    timeout = setTimeout(fail, 30_000);
    if (start) {
      Promise.resolve().then(start, fail).catch(fail);
    } else {
      chrome.tabs.get(tabId).then((tab) => {
        if (tab.status === "complete") complete();
      }, fail);
    }
  });
}

async function authenticatedShopTab() {
  const tabs = await chrome.tabs.query({ url: REWE_ORIGINS });
  let tab = tabs.find((candidate) => candidate.url?.startsWith("https://www.rewe.de/shop"));
  if (!tab) {
    tab = await chrome.tabs.create({ url: "https://www.rewe.de/shop?loggedIn=1", active: true });
    await waitForNavigation(tab.id, null, false);
  } else {
    await chrome.tabs.update(tab.id, { active: true });
    await waitForNavigation(tab.id, () => chrome.tabs.reload(tab.id), true);
  }
  return tab;
}

// The one Chrome Native Messaging port kept open for the lifetime of the
// bridge. Re-clicking the action replaces it; a stale port's onDisconnect
// must not clobber a newer one, so handlers below re-check identity against
// this variable before acting.
let nativePort = null;
let reconnectTimer = null;
let reconnecting = false;
let manualConnectPromise = null;
let manualConnectGeneration = 0;

// A freshly created tab's "complete" status (waitForNavigation) doesn't
// guarantee the content script has finished registering its message
// listener yet — observed live (2026-08-19): the very first operation after
// tab creation can hit "Could not establish connection" even though the
// page itself has loaded, most likely a race with REWE's own loggedIn=1
// redirect re-injecting the script on a second document. Retried rather
// than guessed at REWE's exact redirect timing.
async function sendToContentScript(tabId, message) {
  // A rejected response channel does not prove whether a mutation reached
  // REWE. Reads may retry; mutations must surface the ambiguity once.
  const attempts = MUTATING_OPERATIONS.has(message?.operation) ? 1 : 3;
  const retryDelayMs = 300;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      return await withTimeout(
        chrome.tabs.sendMessage(tabId, message),
        CONTENT_SCRIPT_RESPONSE_TIMEOUT_MS,
      );
    } catch (error) {
      if (error?.message === "operation_timeout") throw error;
      if (attempt === attempts) throw error;
      await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
    }
  }
}

async function withTimeout(promise, timeoutMs) {
  let timeout;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timeout = setTimeout(() => reject(new Error("operation_timeout")), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timeout);
  }
}

function bindPort(port, tabId) {
  nativePort = port;

  port.onMessage.addListener(async (message) => {
    if (message?.type !== "operation_request") return;
    const { request_id, operation, params } = message;

    let response;
    try {
      response = await sendToContentScript(tabId, { operation, params });
    } catch (error) {
      response = {
        ok: false,
        code: error?.message === "operation_timeout" ? "operation_timeout" : "content_script_unreachable",
      };
    }

    port.postMessage({
      version: 2,
      type: "operation_response",
      request_id,
      ok: !!response?.ok,
      ...(response?.ok ? { result: response.result } : { code: response?.code ?? "content_script_unreachable" }),
    });

    if (!response?.ok && response.code === "content_script_unreachable") {
      await setErrorBadge("content_script_unreachable");
    }
  });

  port.onDisconnect.addListener(async () => {
    if (nativePort !== port) return;
    nativePort = null;
    scheduleReconnect();
    await setErrorBadge("native_host_error");
  });
}

function scheduleReconnect() {
  armReconnectWatchdog();
  if (reconnectTimer !== null) return;
  reconnectTimer = setTimeout(() => {
    reconnectTimer = null;
    reconnectNativeHost();
  }, RECONNECT_DELAY_MS);
}

function armReconnectWatchdog() {
  chrome.alarms.create(RECONNECT_ALARM, {
    delayInMinutes: RECONNECT_WATCHDOG_MINUTES,
    periodInMinutes: RECONNECT_WATCHDOG_MINUTES,
  });
}

function replaceNativePort(port, tabId) {
  const previousPort = nativePort;
  nativePort = null;
  if (previousPort) previousPort.disconnect();
  bindPort(port, tabId);
}

async function reconnectNativeHost() {
  if (nativePort || reconnecting || manualConnectPromise) return;
  const generation = manualConnectGeneration;
  reconnecting = true;
  try {
    const granted = await chrome.permissions.contains({ origins: REWE_ORIGINS });
    if (!granted) return;
    if (nativePort || manualConnectPromise || generation !== manualConnectGeneration) return;
    const tabs = await chrome.tabs.query({ url: REWE_ORIGINS });
    const tab = tabs.find((candidate) => candidate.url?.startsWith("https://www.rewe.de/shop"));
    if (!tab) return;
    if (nativePort || manualConnectPromise || generation !== manualConnectGeneration) return;

    const port = chrome.runtime.connectNative(HOST_NAME);
    replaceNativePort(port, tab.id);
    await setBadge("OK", "#137333");
    await chrome.action.setTitle({ title: "grocery-mcp: connected" });
  } catch {
    await setErrorBadge("native_host_error");
  } finally {
    reconnecting = false;
  }
}

async function connect() {
  if (manualConnectPromise) return manualConnectPromise;
  manualConnectGeneration++;
  manualConnectPromise = connectManually();
  try {
    return await manualConnectPromise;
  } finally {
    manualConnectPromise = null;
  }
}

async function connectManually() {
  const granted = await chrome.permissions.request({ origins: REWE_ORIGINS });
  if (!granted) throw new Error("permission_denied");

  const tab = await authenticatedShopTab();

  let port;
  try {
    port = chrome.runtime.connectNative(HOST_NAME);
  } catch {
    throw new Error("native_host_error");
  }
  replaceNativePort(port, tab.id);
  armReconnectWatchdog();
}

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === RECONNECT_ALARM) reconnectNativeHost();
});

chrome.runtime.onStartup.addListener(scheduleReconnect);

chrome.action.onClicked.addListener(async () => {
  try {
    await connect();
    await setBadge("OK", "#137333");
    await chrome.action.setTitle({ title: "grocery-mcp: connected" });
  } catch (error) {
    await setErrorBadge(safeErrorCode(error));
  }
});
