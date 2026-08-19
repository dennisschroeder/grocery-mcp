package mcpserver

import "github.com/dennisschroeder/grocery-mcp/internal/shopping"

// Money and moneyOutput are shared across this package's tool files
// (basket.go, orders.go) — each vertical's isolated worktree wrote its own
// copy independently; consolidated at fan-in since Go doesn't allow two
// same-named declarations in one package.
type Money struct {
	Cents    int64  `json:"cents" jsonschema:"amount in integer minor units (cents)"`
	Currency string `json:"currency" jsonschema:"ISO 4217 currency code"`
}

func moneyOutput(money shopping.Money) Money {
	return Money{Cents: money.Cents, Currency: money.Currency}
}
