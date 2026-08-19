const HOST_NAME = "com.dennisschroeder.grocery_mcp";
const REWE_ORIGINS = ["https://www.rewe.de/*"];

async function setBadge(text, color) {
  await chrome.action.setBadgeBackgroundColor({ color });
  await chrome.action.setBadgeText({ text });
}

const SAFE_ERROR_CODES = new Set([
  "permission_denied",
  "shop_reload_failed",
  "native_host_error",
  "content_script_unreachable",
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

// A freshly created tab's "complete" status (waitForNavigation) doesn't
// guarantee the content script has finished registering its message
// listener yet — observed live (2026-08-19): the very first operation after
// tab creation can hit "Could not establish connection" even though the
// page itself has loaded, most likely a race with REWE's own loggedIn=1
// redirect re-injecting the script on a second document. Retried rather
// than guessed at REWE's exact redirect timing.
async function sendToContentScript(tabId, message) {
  const attempts = 3;
  const retryDelayMs = 300;
  for (let attempt = 1; attempt <= attempts; attempt++) {
    try {
      return await chrome.tabs.sendMessage(tabId, message);
    } catch (error) {
      if (attempt === attempts) throw error;
      await new Promise((resolve) => setTimeout(resolve, retryDelayMs));
    }
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
    } catch {
      response = { ok: false, code: "content_script_unreachable" };
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
    await setErrorBadge("native_host_error");
  });
}

async function connect() {
  const granted = await chrome.permissions.request({ origins: REWE_ORIGINS });
  if (!granted) throw new Error("permission_denied");

  const tab = await authenticatedShopTab();

  if (nativePort) nativePort.disconnect();

  let port;
  try {
    port = chrome.runtime.connectNative(HOST_NAME);
  } catch {
    throw new Error("native_host_error");
  }
  bindPort(port, tab.id);
}

chrome.action.onClicked.addListener(async () => {
  try {
    await connect();
    await setBadge("OK", "#137333");
    await chrome.action.setTitle({ title: "grocery-mcp: connected" });
  } catch (error) {
    await setErrorBadge(safeErrorCode(error));
  }
});
