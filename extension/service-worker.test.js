const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

function loadServiceWorker(sendMessage) {
  const context = vm.createContext({
    chrome: {
      action: {
        onClicked: { addListener() {} },
        setBadgeBackgroundColor: async () => {},
        setBadgeText: async () => {},
        setTitle: async () => {},
      },
      permissions: { request: async () => true },
      runtime: { connectNative() {} },
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
