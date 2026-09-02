const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

function loadServiceWorker(sendMessage) {
  const eventListeners = {};
  const context = vm.createContext({
    eventListeners,
    chrome: {
      action: {
        onClicked: { addListener() {} },
        setBadgeBackgroundColor: async () => {},
        setBadgeText: async () => {},
        setTitle: async () => {},
      },
      alarms: {
        clear: async () => {},
        create() {},
        onAlarm: { addListener(listener) { eventListeners.alarm = listener; } },
      },
      permissions: {
        contains: async () => true,
        request: async () => true,
      },
      runtime: {
        connectNative() {},
        onStartup: { addListener(listener) { eventListeners.startup = listener; } },
      },
      tabs: {
        create: async () => ({ id: 1 }),
        get: async () => ({ status: "complete" }),
        onRemoved: { addListener() {}, removeListener() {} },
        onUpdated: { addListener() {}, removeListener() {} },
        query: async () => [],
        reload: async () => {},
        sendMessage,
        update: async () => {},
      },
    },
    clearTimeout,
    Promise,
    setTimeout: (callback, delay) => setTimeout(callback, Math.min(delay, 5)),
  });
  const source = fs.readFileSync(`${__dirname}/service-worker.js`, "utf8");
  vm.runInContext(source, context);
  return context;
}

function fakePort() {
  const listeners = {};
  const messages = [];
  return {
    listeners,
    messages,
    onDisconnect: { addListener(listener) { listeners.disconnect = listener; } },
    onMessage: { addListener(listener) { listeners.message = listener; } },
    disconnect() { listeners.disconnect?.(); },
    postMessage(message) { messages.push(message); },
  };
}

test("bounds a content-script operation that never responds", async () => {
  let calls = 0;
  const context = loadServiceWorker(() => {
    calls++;
    return new Promise(() => {});
  });

  const outcome = await Promise.race([
    context.sendToContentScript(1, { operation: "basket_apply" }).then(
      () => "resolved",
      (error) => error.message,
    ),
    new Promise((resolve) => setTimeout(() => resolve("test_timeout"), 50)),
  ]);

  assert.equal(outcome, "operation_timeout");
  assert.equal(calls, 1);
});

test("returns operation_timeout to the native host", async () => {
  const context = loadServiceWorker(() => new Promise(() => {}));
  const port = fakePort();
  context.bindPort(port, 1);

  await port.listeners.message({
    type: "operation_request",
    request_id: "request-1",
    operation: "basket_apply",
    params: {},
  });

  assert.deepEqual(JSON.parse(JSON.stringify(port.messages)), [{
    version: 2,
    type: "operation_response",
    request_id: "request-1",
    ok: false,
    code: "operation_timeout",
  }]);
});

test("does not retry a mutation after the response channel fails", async () => {
  let calls = 0;
  const context = loadServiceWorker(async () => {
    calls++;
    throw new Error("The message port closed before a response was received.");
  });
  const port = fakePort();
  context.bindPort(port, 1);

  await port.listeners.message({
    type: "operation_request",
    request_id: "request-1",
    operation: "basket_apply",
    params: {},
  });

  assert.equal(calls, 1);
  assert.equal(port.messages[0].code, "content_script_unreachable");
});

test("retries a read when the content script is still loading", async () => {
  let calls = 0;
  const context = loadServiceWorker(async () => {
    calls++;
    if (calls === 1) throw new Error("Could not establish connection.");
    return { ok: true, result: { items: [] } };
  });

  const result = await context.sendToContentScript(1, { operation: "basket_get" });

  assert.equal(calls, 2);
  assert.deepEqual(JSON.parse(JSON.stringify(result)), { ok: true, result: { items: [] } });
});

test("reconnects the native host after disconnect without another permission prompt or tab reload", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  const firstPort = fakePort();
  let permissionRequests = 0;
  let nativeConnections = 0;
  let createdTabs = 0;
  let reloads = 0;
  let updatedTabs = 0;

  context.chrome.permissions.request = async () => {
    permissionRequests++;
    return true;
  };
  context.chrome.permissions.contains = async () => true;
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return fakePort();
  };
  context.chrome.tabs.query = async () => [{ id: 1, url: "https://www.rewe.de/shop" }];
  context.chrome.tabs.create = async () => {
    createdTabs++;
    return { id: 2 };
  };
  context.chrome.tabs.reload = async () => { reloads++; };
  context.chrome.tabs.update = async () => { updatedTabs++; };

  context.bindPort(firstPort, 1);
  await firstPort.listeners.disconnect();
  await new Promise((resolve) => setTimeout(resolve, 20));

  assert.equal(nativeConnections, 1);
  assert.equal(permissionRequests, 0);
  assert.equal(createdTabs, 0);
  assert.equal(reloads, 0);
  assert.equal(updatedTabs, 0);
});

test("watchdog alarm reconnects when the in-memory retry was lost", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  let nativeConnections = 0;
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return fakePort();
  };
  context.chrome.tabs.query = async () => [{ id: 1, url: "https://www.rewe.de/shop" }];

  await context.eventListeners.alarm({ name: "native-host-reconnect" });
  await new Promise((resolve) => setTimeout(resolve, 20));

  assert.equal(nativeConnections, 1);
});

test("automatic reconnect never requests a missing host permission", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  const firstPort = fakePort();
  let permissionRequests = 0;
  let nativeConnections = 0;
  let tabQueries = 0;
  context.chrome.permissions.contains = async () => false;
  context.chrome.permissions.request = async () => {
    permissionRequests++;
    return true;
  };
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return fakePort();
  };
  context.chrome.tabs.query = async () => {
    tabQueries++;
    return [];
  };

  context.bindPort(firstPort, 1);
  await firstPort.listeners.disconnect();
  await context.eventListeners.alarm({ name: "native-host-reconnect" });
  await new Promise((resolve) => setTimeout(resolve, 20));

  assert.equal(nativeConnections, 0);
  assert.equal(permissionRequests, 0);
  assert.equal(tabQueries, 0);
});

test("coalesces repeated reconnect scheduling into one native connection", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  let nativeConnections = 0;
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return fakePort();
  };
  context.chrome.tabs.query = async () => [{ id: 1, url: "https://www.rewe.de/shop" }];

  context.scheduleReconnect();
  context.scheduleReconnect();
  await context.eventListeners.alarm({ name: "native-host-reconnect" });
  await new Promise((resolve) => setTimeout(resolve, 20));

  assert.equal(nativeConnections, 1);
});

test("a stale port disconnect cannot replace or reconnect a newer port", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  const firstPort = fakePort();
  const secondPort = fakePort();
  let nativeConnections = 0;
  let reconnectSchedules = 0;
  context.chrome.alarms.create = () => { reconnectSchedules++; };
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return fakePort();
  };

  context.bindPort(firstPort, 1);
  context.replaceNativePort(secondPort, 1);
  await firstPort.listeners.disconnect();
  await context.reconnectNativeHost();

  assert.equal(nativeConnections, 0);
  assert.equal(reconnectSchedules, 0);
});

test("a manual connection supersedes an overlapping automatic reconnect", async () => {
  const context = loadServiceWorker(async () => ({ ok: true, result: {} }));
  let releasePermissionCheck;
  const permissionCheck = new Promise((resolve) => { releasePermissionCheck = resolve; });
  const manualPort = fakePort();
  let nativeConnections = 0;

  context.chrome.permissions.contains = () => permissionCheck;
  context.chrome.tabs.query = async () => [];
  context.chrome.runtime.connectNative = () => {
    nativeConnections++;
    return manualPort;
  };

  const automatic = context.reconnectNativeHost();
  await Promise.resolve();
  await context.connect();
  releasePermissionCheck(true);
  await automatic;

  assert.equal(nativeConnections, 1);
  assert.equal(typeof manualPort.listeners.message, "function");
});
