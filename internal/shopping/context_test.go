package shopping

import "testing"

func fullContext() ShoppingContext {
	return ShoppingContext{
		AccountID:     "account-1",
		ShopSessionID: "session-1",
		StoreID:       "store-1",
		PostalCode:    "10115",
		BasketID:      "basket-1",
		TimeSlotID:    "timeslot-1",
	}
}

func TestWithAccountInvalidatesEverything(t *testing.T) {
	got := fullContext().WithAccount("account-2")
	want := ShoppingContext{AccountID: "account-2"}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWithAccountUnchangedIsNoOp(t *testing.T) {
	c := fullContext()
	if got := c.WithAccount(c.AccountID); got != c {
		t.Fatalf("unchanged account mutated context: %#v", got)
	}
}

func TestWithSessionPreservesStoreBasketTimeSlot(t *testing.T) {
	c := fullContext()
	got := c.WithSession("session-2")
	want := c
	want.ShopSessionID = "session-2"
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWithSessionUnchangedIsNoOp(t *testing.T) {
	c := fullContext()
	if got := c.WithSession(c.ShopSessionID); got != c {
		t.Fatalf("unchanged session mutated context: %#v", got)
	}
}

func TestWithStoreInvalidatesBasketAndTimeSlot(t *testing.T) {
	c := fullContext()
	got := c.WithStore("store-2")
	want := ShoppingContext{AccountID: c.AccountID, ShopSessionID: c.ShopSessionID, StoreID: "store-2"}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWithStoreUnchangedIsNoOp(t *testing.T) {
	c := fullContext()
	if got := c.WithStore(c.StoreID); got != c {
		t.Fatalf("unchanged store mutated context: %#v", got)
	}
}

// TestWithBasketInvalidatesTimeSlotOnly also proves PostalCode survives a
// basket rebind — WithBasket's fresh-struct-literal predecessor silently
// dropped it (caught by review before shipping), which would have broken
// ListTimeSlots after any successful basket_apply.
func TestWithBasketInvalidatesTimeSlotOnly(t *testing.T) {
	c := fullContext()
	got := c.WithBasket("basket-2")
	want := ShoppingContext{AccountID: c.AccountID, ShopSessionID: c.ShopSessionID, StoreID: c.StoreID, PostalCode: c.PostalCode, BasketID: "basket-2"}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWithBasketUnchangedIsNoOp(t *testing.T) {
	c := fullContext()
	if got := c.WithBasket(c.BasketID); got != c {
		t.Fatalf("unchanged basket mutated context: %#v", got)
	}
}

func TestWithTimeSlotInvalidatesNothing(t *testing.T) {
	c := fullContext()
	got := c.WithTimeSlot("timeslot-2")
	want := c
	want.TimeSlotID = "timeslot-2"
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestWithTimeSlotUnchangedIsNoOp(t *testing.T) {
	c := fullContext()
	if got := c.WithTimeSlot(c.TimeSlotID); got != c {
		t.Fatalf("unchanged timeslot mutated context: %#v", got)
	}
}
