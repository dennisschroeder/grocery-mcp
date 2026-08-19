package mcpserver

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
)

type StoresSearchInput struct {
	PostalCode string `json:"postal_code" jsonschema:"German postal code to search for nearby REWE pickup markets"`
}

// Street-level address is deliberately never surfaced here — see AGENTS.md's
// "never commit... addresses" rule and gateway_stores.go's storesSearchStore.
type StoreResult struct {
	ID             string  `json:"id" jsonschema:"REWE market ID"`
	Name           string  `json:"name,omitempty" jsonschema:"store display name, when known"`
	PostalCode     string  `json:"postal_code,omitempty" jsonschema:"postal code associated with this market, when known"`
	City           string  `json:"city,omitempty" jsonschema:"city, when known"`
	DistanceMeters float64 `json:"distance_meters,omitempty" jsonschema:"distance from the searched postal code in meters, when known"`
	ObservedAt     string  `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type StoresSearchOutput struct {
	Stores     []StoreResult `json:"stores" jsonschema:"markets discovered for the searched postal code"`
	HasMore    bool          `json:"has_more" jsonschema:"whether more stores exist beyond this page"`
	ObservedAt string        `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type StoreSelectInput struct {
	StoreID    string `json:"store_id" jsonschema:"REWE market ID to bind as the active store"`
	PostalCode string `json:"postal_code" jsonschema:"postal code this market was found under (from stores_search) — REWE's timeslot endpoint requires it alongside the market ID"`
}

type StoreSelection struct {
	StoreID string `json:"store_id" jsonschema:"the market ID now bound to the shopping context"`
}

type ProductsSearchInput struct {
	Query string `json:"query" jsonschema:"search term"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum number of products to return (1-50); omit for the default"`
}

type ProductResult struct {
	ID            string `json:"id" jsonschema:"REWE product ID"`
	Name          string `json:"name" jsonschema:"product display name"`
	Brand         string `json:"brand,omitempty" jsonschema:"product brand, when known"`
	Unit          string `json:"unit,omitempty" jsonschema:"unit or package size, when known"`
	PriceCents    int64  `json:"price_cents" jsonschema:"price in the smallest currency unit"`
	PriceCurrency string `json:"price_currency" jsonschema:"ISO 4217 currency code"`
	Available     bool   `json:"available" jsonschema:"whether REWE reports the product as available"`
	ObservedAt    string `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

type ProductsSearchOutput struct {
	Products   []ProductResult `json:"products" jsonschema:"matching products"`
	HasMore    bool            `json:"has_more" jsonschema:"whether more results exist beyond this page"`
	ObservedAt string          `json:"observed_at" jsonschema:"UTC observation time in RFC 3339 format"`
}

// RegisterStoresTools registers the stores/products vertical's MCP tools.
// Wiring this into server.go's New() is the fan-in step, not this
// function's job.
func RegisterStoresTools(server *mcp.Server, core *shopping.Core) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "stores_search",
		Description: "Search REWE pickup markets near a postal code, nearest first. Returns real market IDs, " +
			"names, and addresses from REWE's own store locator.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input StoresSearchInput) (*mcp.CallToolResult, StoresSearchOutput, error) {
		page, err := core.StoresSearch(ctx, shopping.StoreSearch{PostalCode: input.PostalCode})
		if err != nil {
			return nil, StoresSearchOutput{}, err
		}
		return nil, storesSearchOutput(page), nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "store_select",
		Description: "Bind a REWE market ID as the active store for this shopping context. Selecting a " +
			"different store clears any previously selected basket and timeslot, since both are priced and " +
			"scoped to a store.",
		Annotations: annotations(false, false, false),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input StoreSelectInput) (*mcp.CallToolResult, StoreSelection, error) {
		next, err := core.SelectStore(ctx, shopping.StoreID(input.StoreID), input.PostalCode)
		if err != nil {
			return nil, StoreSelection{}, err
		}
		return nil, StoreSelection{StoreID: string(next.StoreID)}, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "products_search",
		Description: "Search REWE products by term within the currently selected store (call store_select " +
			"first). Pagination beyond a single page is not supported in this version (v1 limitation): a " +
			"nonzero offset is rejected rather than silently returning the first page again.",
		Annotations: annotations(true, false, true),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, input ProductsSearchInput) (*mcp.CallToolResult, ProductsSearchOutput, error) {
		page, err := core.ProductsSearch(ctx, shopping.ProductSearch{Query: input.Query, Limit: input.Limit})
		if err != nil {
			return nil, ProductsSearchOutput{}, err
		}
		return nil, productsSearchOutput(page), nil
	})
}

func storesSearchOutput(page shopping.StorePage) StoresSearchOutput {
	stores := make([]StoreResult, 0, len(page.Stores))
	for _, store := range page.Stores {
		stores = append(stores, StoreResult{
			ID:             string(store.ID),
			Name:           store.Name,
			PostalCode:     store.Address.PostalCode,
			City:           store.Address.City,
			DistanceMeters: store.DistanceMeters,
			ObservedAt:     store.ObservedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return StoresSearchOutput{Stores: stores, HasMore: page.HasMore, ObservedAt: page.ObservedAt.UTC().Format(time.RFC3339Nano)}
}

func productsSearchOutput(page shopping.ProductPage) ProductsSearchOutput {
	products := make([]ProductResult, 0, len(page.Products))
	for _, product := range page.Products {
		products = append(products, ProductResult{
			ID:            string(product.ID),
			Name:          product.Name,
			Brand:         product.Brand,
			Unit:          product.Unit,
			PriceCents:    product.Price.Cents,
			PriceCurrency: product.Price.Currency,
			Available:     product.Available,
			ObservedAt:    product.ObservedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return ProductsSearchOutput{Products: products, HasMore: page.HasMore, ObservedAt: page.ObservedAt.UTC().Format(time.RFC3339Nano)}
}
