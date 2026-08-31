const assert = require("node:assert/strict");
const fs = require("node:fs");
const test = require("node:test");
const vm = require("node:vm");

function loadContentScript() {
  const context = vm.createContext({
    chrome: { runtime: { onMessage: { addListener() {} } } },
    decodeURIComponent,
    fetch: async () => {
      throw new Error("unexpected fetch");
    },
    performance: { getEntriesByType: () => [] },
    Promise,
    Set,
    URL,
  });
  const source = fs.readFileSync(`${__dirname}/content-script.js`, "utf8");
  vm.runInContext(source, context);
  return context.listingIDsFromResourceEntries;
}

const listingIDsFromResourceEntries = loadContentScript();

test("extracts both basket listing URL forms newest first", () => {
  const entries = [
    { name: "https://www.rewe.de/shop/api/baskets/basket-1/listings/listing-old?includeTimeslot=true" },
    { name: "https://www.rewe.de/shop/api/baskets/listings/listing-new?includeTimeslot=true" },
  ];

  assert.deepEqual(Array.from(listingIDsFromResourceEntries(entries)), ["listing-new", "listing-old"]);
});

test("deduplicates listing IDs", () => {
  const entries = [
    { name: "https://www.rewe.de/shop/api/baskets/listings/listing-1" },
    { name: "https://www.rewe.de/shop/api/baskets/basket-1/listings/listing-1" },
  ];

  assert.deepEqual(Array.from(listingIDsFromResourceEntries(entries)), ["listing-1"]);
});

test("rejects malformed and unsafe listing IDs", () => {
  const entries = [
    { name: "https://www.rewe.de/shop/api/baskets/listings/listing%2Funsafe" },
    { name: `https://www.rewe.de/shop/api/baskets/listings/${"x".repeat(201)}` },
    { name: "https://www.rewe.de/shop/api/baskets/listings/%E0%A4%A" },
  ];

  assert.deepEqual(Array.from(listingIDsFromResourceEntries(entries)), []);
});

test("rejects matching basket paths from other origins", () => {
  const entries = [{ name: "https://tracker.example/shop/api/baskets/listings/fake-listing" }];

  assert.deepEqual(Array.from(listingIDsFromResourceEntries(entries)), []);
});

test("returns an empty list without basket activity", () => {
  assert.deepEqual(Array.from(listingIDsFromResourceEntries([{ name: "https://www.rewe.de/shop/api/favorites" }])), []);
});
