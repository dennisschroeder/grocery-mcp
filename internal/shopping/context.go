package shopping

// Rebinding methods on ShoppingContext, per AGENTS.md: "Bind account, store,
// basket, and session context; changing one invalidates dependent state."
// Store, basket, and timeslot form a strict chain — a REWE basket is priced
// and scoped to one store, and a reserved timeslot is scoped to the basket
// it was reserved for — so WithStore drops basket+timeslot and WithBasket
// drops timeslot. Session sits outside that chain: auth.Service never
// rotates ShopSessionID on a refresh (only a genuinely new Connect does,
// which already starts from a wiped, Unauthenticated context), and REWE's
// basket/store selection is scoped to the account, not the short-lived
// session identity — so WithSession alone invalidates nothing. WithAccount
// invalidates everything, session included. All are no-ops when the value
// is unchanged, so idempotent reselection never wipes a valid basket or
// timeslot.

// WithAccount rebinds to a different Account Identity. Nothing about a
// previous account's session, store, basket, or timeslot carries over.
func (c ShoppingContext) WithAccount(id AccountID) ShoppingContext {
	if id == c.AccountID {
		return c
	}
	return ShoppingContext{AccountID: id}
}

// WithSession rebinds the Shop Session Identity within the same account.
// A session change (e.g. after browser-assisted reauthentication) does not
// by itself invalidate store, basket, or timeslot bindings — REWE state
// there is scoped to the account, not the short-lived session identity.
func (c ShoppingContext) WithSession(id ShopSessionID) ShoppingContext {
	if id == c.ShopSessionID {
		return c
	}
	c.ShopSessionID = id
	return c
}

// WithStore rebinds the selected store, invalidating basket and timeslot —
// both are priced and scoped to a specific store.
func (c ShoppingContext) WithStore(id StoreID) ShoppingContext {
	if id == c.StoreID {
		return c
	}
	return ShoppingContext{AccountID: c.AccountID, ShopSessionID: c.ShopSessionID, StoreID: id}
}

// WithPostalCode attaches the postal code a store selection was made with.
// REWE's timeslot endpoint requires it alongside the market ID as a request
// header; it carries no invalidation semantics of its own — it is
// only ever set together with StoreID by SelectStore, and WithStore already
// clears it (a fresh struct literal) whenever the store itself changes.
func (c ShoppingContext) WithPostalCode(postalCode string) ShoppingContext {
	if postalCode == c.PostalCode {
		return c
	}
	c.PostalCode = postalCode
	return c
}

// WithBasket rebinds the active basket, invalidating the timeslot — a
// reserved pickup slot is tied to the basket it was reserved for. PostalCode
// is untouched: it travels with the store, not the basket, and a fresh
// struct literal here (like WithStore's) would silently drop it on every
// basket_apply — caught by review before it shipped.
func (c ShoppingContext) WithBasket(id BasketID) ShoppingContext {
	if id == c.BasketID {
		return c
	}
	c.BasketID = id
	c.TimeSlotID = ""
	return c
}

// WithTimeSlot rebinds the selected pickup timeslot. Nothing depends on it.
func (c ShoppingContext) WithTimeSlot(id TimeSlotID) ShoppingContext {
	if id == c.TimeSlotID {
		return c
	}
	c.TimeSlotID = id
	return c
}
