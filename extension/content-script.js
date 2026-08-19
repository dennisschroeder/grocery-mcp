async function fetchWithStandardMapping(url, extraHeaders) {
  let response;
  try {
    response = await fetch(url, { credentials: "same-origin", headers: extraHeaders });
  } catch {
    return { ok: false, code: "upstream_changed" };
  }

  if (response.status === 401 || response.status === 403) return { ok: false, code: "auth_invalid" };
  if (response.status === 429) return { ok: false, code: "rate_limited" };
  if (response.status !== 200) return { ok: false, code: "upstream_changed" };

  try {
    return { ok: true, result: await response.json() };
  } catch {
    return { ok: false, code: "malformed_response" };
  }
}

function handleSessionIdentity() {
  return fetchWithStandardMapping("/shop/api/favorites");
}

// Validates params before any URL is built — an out-of-range or wrong-typed
// field is rejected here, never interpolated into a fetch target.
function validatedSearchParams(params) {
  const term = params?.term;
  const marketId = params?.market_id;
  if (typeof term !== "string" || term.trim() === "" || term.length > 200) return null;
  if (typeof marketId !== "string" || !/^[0-9]+$/.test(marketId)) return null;
  const objectsPerPage =
    Number.isInteger(params?.objects_per_page) && params.objects_per_page > 0 && params.objects_per_page <= 50
      ? params.objects_per_page
      : 5;
  return { term, marketId, objectsPerPage };
}

// PRODUCT_ACCEPT is the Accept type REWE's /products endpoint actually
// checks — captured directly from a live rewe.de search request in
// DevTools (2026-08-19), not from karrt's source: karrt's own claimed
// param names (search=/market=/serviceTypes=) and Accept value
// (application/vnd.rewe.productlist+json) both turned out wrong against
// real traffic. The endpoint still returns 200 with an empty products
// array on a wrong Accept header, regardless of query params, which is
// what silently broke this handler either way.
const PRODUCT_ACCEPT = { Accept: "application/vnd.rewe.digital.products+json;client=web;version=2" };

function handleProductsSearch(params) {
  const validated = validatedSearchParams(params);
  if (!validated) return Promise.resolve({ ok: false, code: "invalid_params" });
  const url =
    `/shop/api/products?term=${encodeURIComponent(validated.term)}` +
    `&autoCompletion=true&objectsPerPage=${validated.objectsPerPage}` +
    `&marketId=${encodeURIComponent(validated.marketId)}&serviceType=PICKUP`;
  return fetchWithStandardMapping(url, PRODUCT_ACCEPT);
}

// REWE's real store-locator endpoint — discovered live (2026-08-19) by
// capturing REWE's own "change market" flow directly in DevTools. Replaces
// an earlier heuristic (abusing /products search plus a listing-ID suffix
// regex, per an unverified Tobi4s1337/karrt technique) that was confirmed
// broken: it never found a real market ID, even in dense-coverage areas.
// GET /api/marketselection/zipcodes/{zip}/services/pickup returns a bare
// array of nearby pickup-capable markets with real wwIdent market IDs,
// names, and addresses — no discovery hack needed, and no special headers
// either (unlike /shop/api/products, this isn't even under /shop/api/).
function validatedStoreSearchParams(params) {
  const postalCode = params?.postal_code;
  if (typeof postalCode !== "string" || !/^[0-9]{5}$/.test(postalCode)) return null;
  return { postalCode };
}

function handleStoresSearch(params) {
  const validated = validatedStoreSearchParams(params);
  if (!validated) return Promise.resolve({ ok: false, code: "invalid_params" });
  const url = `/api/marketselection/zipcodes/${encodeURIComponent(validated.postalCode)}/services/pickup`;
  return fetchWithStandardMapping(url).then((response) => {
    if (!response.ok) return response;
    const markets = Array.isArray(response.result) ? response.result : [];
    return {
      ok: true,
      result: {
        // Street-level address is deliberately never forwarded here — the
        // project's own fixture sanitizer treats "street"/"address" as
        // sensitive fields (AGENTS.md: "never commit... addresses"), even
        // though REWE's response does include one.
        stores: markets.map((market) => ({
          market_id: typeof market?.wwIdent === "string" ? market.wwIdent : "",
          name: typeof market?.displayName === "string" ? market.displayName : "",
          postal_code: typeof market?.zipCode === "string" ? market.zipCode : validated.postalCode,
          city: typeof market?.city === "string" ? market.city : "",
          distance_meters: typeof market?.distance === "number" ? market.distance : null,
        })),
      },
    };
  });
}

// No REWE-side pagination parameters are confirmed for /orders (see
// .agents/phase1-verticals/contract.md), so this fetches the full list
// literally and lets the Go side page the decoded result.
function handleOrdersList() {
  return fetchWithStandardMapping("/shop/api/orders");
}

// Only URL-safe identifier characters are allowed through — rejected before
// the template string is ever built, same as validatedSearchParams.
function validatedOrderIdParams(params) {
  const orderId = params?.order_id;
  if (typeof orderId !== "string" || orderId.trim() === "" || orderId.length > 100) return null;
  if (!/^[A-Za-z0-9_-]+$/.test(orderId)) return null;
  return { orderId };
}

function handleOrderGet(params) {
  const validated = validatedOrderIdParams(params);
  if (!validated) return Promise.resolve({ ok: false, code: "invalid_params" });
  const url = `/shop/api/orders/${encodeURIComponent(validated.orderId)}`;
  return fetchWithStandardMapping(url);
}

// Headers REWE's basket API requires beyond the default same-origin fetch
// (card #8's contract research: x-application-id + the basket-v2 Accept
// type). Shared by basket_get and basket_apply's per-item calls.
const BASKET_HEADERS = {
  "x-application-id": "rewe-basket",
  "Accept": "application/vnd.com.rewe.digital.basket-v2+json",
};

// Tobi4s1337/karrt's basketRemove uses this x-origin variant specifically
// for DELETE, distinct from add's plain BASKET_HEADERS — confirmed against
// its source, not guessed.
const BASKET_OVERVIEW_HEADERS = { ...BASKET_HEADERS, "x-origin": "BASKET_OVERVIEW" };

function validatedBasketId(basketId) {
  if (typeof basketId !== "string") return null;
  if (!/^[A-Za-z0-9._-]{1,128}$/.test(basketId)) return null;
  return basketId;
}

// fetchJSONMutation is basket_apply/basket_get's own fetch-and-map helper
// (distinct from fetchWithStandardMapping above): it needs custom headers
// and non-GET methods, and — unlike a read — must not treat a 2xx response
// with no/non-JSON body (typical for DELETE) as malformed_response, since
// the mutation still succeeded.
async function fetchJSONMutation(url, method, body, headers) {
  let response;
  try {
    response = await fetch(url, {
      method,
      credentials: "same-origin",
      headers: body === undefined ? headers : { ...headers, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    return { ok: false, code: "upstream_changed" };
  }

  if (response.status === 401 || response.status === 403) return { ok: false, code: "auth_invalid" };
  if (response.status === 429) return { ok: false, code: "rate_limited" };
  if (response.status < 200 || response.status >= 300) return { ok: false, code: "upstream_changed" };
  if (response.status === 204) return { ok: true, result: null };
  try {
    return { ok: true, result: await response.json() };
  } catch {
    return { ok: true, result: null };
  }
}

function handleBasketGet(params) {
  const basketId = validatedBasketId(params?.basket_id);
  if (!basketId) return Promise.resolve({ ok: false, code: "invalid_params" });
  const url = `/shop/api/baskets/${encodeURIComponent(basketId)}`;
  return fetchJSONMutation(url, "GET", undefined, BASKET_HEADERS);
}

function validatedApplyChange(change) {
  const listingId = change?.listing_id;
  const quantity = change?.quantity;
  if (typeof listingId !== "string" || listingId.trim() === "" || listingId.length > 200) return null;
  if (!Number.isInteger(quantity) || quantity < 0 || quantity > 999) return null;
  return { listingId, quantity };
}

// Applies exactly one line-item change and reports its own outcome — REWE
// has no bulk basket endpoint, so basket_apply loops this per change
// (card #8's contract research) and Go aggregates the per-item results.
async function applyOneBasketChange(basketId, rawChange) {
  const validated = validatedApplyChange(rawChange);
  if (!validated) {
    const listingId = typeof rawChange?.listing_id === "string" ? rawChange.listing_id : "";
    return { listing_id: listingId, ok: false, code: "invalid_params" };
  }
  const { listingId, quantity } = validated;

  if (quantity === 0) {
    const validBasketId = validatedBasketId(basketId);
    if (!validBasketId) return { listing_id: listingId, ok: false, code: "invalid_params" };
    const url = `/shop/api/baskets/${encodeURIComponent(validBasketId)}/listings/${encodeURIComponent(listingId)}?includeTimeslot=true`;
    const outcome = await fetchJSONMutation(url, "DELETE", undefined, BASKET_OVERVIEW_HEADERS);
    return { listing_id: listingId, ...outcome };
  }

  const url = `/shop/api/baskets/listings/${encodeURIComponent(listingId)}`;
  const outcome = await fetchJSONMutation(url, "POST", { quantity, includeTimeslot: false, context: "product-list-category" }, BASKET_HEADERS);
  return { listing_id: listingId, ...outcome };
}

function handleBasketApply(params) {
  const basketId = typeof params?.basket_id === "string" ? params.basket_id : "";
  const changes = Array.isArray(params?.changes) ? params.changes : null;
  if (!changes || changes.length === 0 || changes.length > 50) {
    return Promise.resolve({ ok: false, code: "invalid_params" });
  }
  return Promise.all(changes.map((change) => applyOneBasketChange(basketId, change))).then((results) => ({
    ok: true,
    result: { changes: results },
  }));
}

// storeContextHeaders builds the rd-market-id/rd-postcode/rd-service-types
// headers REWE's timeslot endpoint expects — karrt's own reference client
// (src/api/index.ts, storeHeaders) sends the same trio on every store-scoped
// call, confirmed against its source directly. Returns null when
// market_id/postal_code are missing or malformed, so a caller can fail
// closed instead of sending a request REWE would reject anyway.
const STORE_CONTEXT_MARKET_ID_PATTERN = /^[0-9]{4,10}$/;
const STORE_CONTEXT_POSTAL_CODE_PATTERN = /^[0-9]{5}$/;

function storeContextHeaders(params) {
  const marketId = params?.market_id;
  const postalCode = params?.postal_code;
  if (typeof marketId !== "string" || !STORE_CONTEXT_MARKET_ID_PATTERN.test(marketId)) return null;
  if (typeof postalCode !== "string" || !STORE_CONTEXT_POSTAL_CODE_PATTERN.test(postalCode)) return null;
  return { "rd-market-id": marketId, "rd-postcode": postalCode, "rd-service-types": "PICKUP" };
}

// REWE can't return pickup slots without knowing which market — fails
// closed on invalid_params rather than reaching out with no store context.
function handleTimeslotsList(params) {
  const headers = storeContextHeaders(params);
  if (!headers) return Promise.resolve({ ok: false, code: "invalid_params" });
  return fetchWithStandardMapping("/shop/api/timeslots/pickup/overview", headers);
}

// Go never calls timeslot_reserve yet (SelectTimeSlot is stubbed pending a
// resolved customerId source — see gateway_basket.go), but the handler is
// wired for real per the contract's endpoint research, so a future card can
// flip the Go-side stub without touching this file again.
function validatedReserveParams(params) {
  const slotId = params?.slot_id;
  const customerId = params?.customer_id;
  const wwIdent = params?.ww_ident;
  const zipCode = params?.zip_code;
  if (typeof slotId !== "string" || slotId.trim() === "" || slotId.length > 200) return null;
  if (typeof customerId !== "string" || customerId.length > 200) return null;
  if (typeof wwIdent !== "string" || wwIdent.trim() === "" || wwIdent.length > 50) return null;
  if (typeof zipCode !== "string" || !/^[0-9]{4,10}$/.test(zipCode)) return null;
  return { slotId, customerId, wwIdent, zipCode };
}

function handleTimeslotReserve(params) {
  const validated = validatedReserveParams(params);
  if (!validated) return Promise.resolve({ ok: false, code: "invalid_params" });
  const { slotId, customerId, wwIdent, zipCode } = validated;
  return fetchJSONMutation(
    "/shop/api/timeslot-reservations",
    "POST",
    { slotId, customerId, wwIdent, zipCode },
    {},
  );
}

// This switch is the actual security boundary: no operation string outside
// it is ever acted on, and every fetch target above is a literal or built
// only from a validated-params function's fixed, checked fields — never an
// arbitrary caller-supplied URL, method, or header. Each case below calls
// its own handleX function; a case whose handler isn't built yet returns
// not_implemented as a placeholder — replace only that handler's body, never
// restructure this switch, so parallel work on different operations never
// touches the same lines.
chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  switch (message?.operation) {
    case "session_identity":
      handleSessionIdentity().then(sendResponse);
      break;
    case "products_search":
      handleProductsSearch(message?.params).then(sendResponse);
      break;
    case "stores_search":
      handleStoresSearch(message?.params).then(sendResponse);
      break;
    case "basket_get":
      handleBasketGet(message?.params).then(sendResponse);
      break;
    case "basket_apply":
      handleBasketApply(message?.params).then(sendResponse);
      break;
    case "timeslots_list":
      handleTimeslotsList(message?.params).then(sendResponse);
      break;
    case "timeslot_reserve":
      handleTimeslotReserve(message?.params).then(sendResponse);
      break;
    case "orders_list":
      handleOrdersList().then(sendResponse);
      break;
    case "order_get":
      handleOrderGet(message?.params).then(sendResponse);
      break;
    default:
      sendResponse({ ok: false, code: "unknown_operation" });
      break;
  }
  return true;
});
