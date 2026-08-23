package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/dennisschroeder/grocery-mcp/internal/auth"
	"github.com/dennisschroeder/grocery-mcp/internal/browserbridge"
	"github.com/dennisschroeder/grocery-mcp/internal/checkout"
	"github.com/dennisschroeder/grocery-mcp/internal/mcpserver"
	"github.com/dennisschroeder/grocery-mcp/internal/rewe"
	"github.com/dennisschroeder/grocery-mcp/internal/shopping"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	if err := run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) > 1 && strings.HasPrefix(arguments[1], "chrome-extension://") {
		return runNativeHost(arguments[1])
	}
	if len(arguments) < 2 {
		return serveMCP()
	}
	switch arguments[1] {
	case "serve":
		return serveMCP()
	case "install-native-host":
		return installNativeHost(arguments[2:])
	case "bridge-smoke":
		return bridgeSmoke(arguments[2:])
	default:
		return errors.New("unknown command")
	}
}

func serveMCP() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	bridge, err := browserbridge.Listen(browserbridge.DefaultSocketPath())
	if err != nil {
		return fmt.Errorf("start browser bridge: %w", err)
	}
	defer bridge.Close()

	service := auth.NewService(rewe.BrowserValidator{Transport: bridge})
	defer service.Disconnect()
	authenticator := newAutoAcceptingAuthenticator(ctx, service)
	bridgeErrors := make(chan error, 1)
	go func() {
		if err := bridge.Serve(ctx); err != nil {
			bridgeErrors <- err
			cancel()
		}
	}()

	gateway := rewe.Gateway{Transport: bridge}
	checkoutGate := checkout.NewGate()
	defer checkoutGate.Close()
	err = mcpserver.New(shopping.NewCore(authenticator, gateway, gateway, gateway, checkoutGate)).Run(ctx, &mcp.StdioTransport{})
	select {
	case bridgeErr := <-bridgeErrors:
		return fmt.Errorf("browser bridge stopped: %w", bridgeErr)
	default:
		return err
	}
}

func runNativeHost(origin string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("locate user home directory")
	}
	allowedOrigin, err := browserbridge.LoadAllowedOrigin(home, runtime.GOOS)
	if err != nil {
		return err
	}
	return browserbridge.RunNativeHost(context.Background(), origin, allowedOrigin, browserbridge.DefaultSocketPath(), os.Stdin, os.Stdout)
}

func installNativeHost(arguments []string) error {
	flags := flag.NewFlagSet("install-native-host", flag.ContinueOnError)
	extensionID := flags.String("extension-id", "", "ID shown for the unpacked Chrome extension")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("install-native-host accepts only flags")
	}
	binaryPath, err := os.Executable()
	if err != nil {
		return errors.New("locate grocery-mcp executable")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return errors.New("locate user home directory")
	}
	manifestPath, err := browserbridge.InstallNativeHost(home, runtime.GOOS, binaryPath, *extensionID)
	if err != nil {
		return err
	}
	fmt.Println("Native Messaging manifest installed at", manifestPath)
	return nil
}

type adHocOperation struct {
	Operation string          `json:"operation"`
	Params    json.RawMessage `json:"params"`
}

func bridgeSmoke(arguments []string) error {
	flags := flag.NewFlagSet("bridge-smoke", flag.ContinueOnError)
	timeout := flags.Duration("timeout", 5*time.Minute, "time allowed for the browser to open its port and answer session_identity")
	operationsFlag := flags.String("operations", "", `JSON array of {"operation":"...","params":{...}} to also prove ad hoc in the same session (one click covers all of them), e.g. '[{"operation":"stores_search","params":{"postal_code":"10115"}},{"operation":"orders_list","params":{}}]'`)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *timeout <= 0 {
		return errors.New("invalid bridge-smoke arguments")
	}
	var adHocOperations []adHocOperation
	if *operationsFlag != "" {
		if err := json.Unmarshal([]byte(*operationsFlag), &adHocOperations); err != nil {
			return fmt.Errorf("-operations is not a valid JSON array: %w", err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server, err := browserbridge.Listen(browserbridge.DefaultSocketPath())
	if err != nil {
		return fmt.Errorf("start browser bridge: %w", err)
	}
	defer server.Close()
	go func() {
		_ = server.Serve(ctx)
	}()

	fmt.Println("Waiting for the grocery-mcp Chrome extension to open its native-messaging port and answer session_identity...")
	actionContext, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	// Diagnostic pass: call the transport directly so a failure reports its
	// real safeErrorCode, not the auth state machine's generic
	// "validation_failed" (Accept collapses every non-auth error alike).
	// Prints only the failure code or the successful result's byte length —
	// never response content, per this repo's no-raw-payload rule. Reports
	// rather than aborts on rejection — a logged-out session is expected to
	// reject here, and the state-machine check below is what actually
	// proves whether that correctly reaches ReauthRequired rather than
	// hanging or misclassifying.
	result, doErr := server.Do(actionContext, browserbridge.OperationSessionIdentity, nil)
	if doErr != nil {
		var coded browserbridge.CodedError
		if errors.As(doErr, &coded) {
			fmt.Printf("session_identity operation rejected (%s)\n", coded.Code())
		} else {
			fmt.Printf("session_identity operation failed: %v\n", doErr)
		}
	} else {
		fmt.Printf("session_identity operation succeeded (%d bytes of JSON).\n", len(result))
		fmt.Println("Response shape (keys/types only, never values):")
		fmt.Println(describeJSONShape(result))
	}

	for _, op := range adHocOperations {
		opResult, err := server.Do(actionContext, browserbridge.Operation(op.Operation), op.Params)
		if err != nil {
			var coded browserbridge.CodedError
			if errors.As(err, &coded) {
				fmt.Printf("%s operation rejected (%s)\n", op.Operation, coded.Code())
				continue
			}
			fmt.Printf("%s operation failed: %v\n", op.Operation, err)
			continue
		}
		fmt.Printf("%s operation succeeded (%d bytes of JSON).\n", op.Operation, len(opResult))
		fmt.Println("Response shape (keys/types only, never values):")
		fmt.Println(describeJSONShape(opResult))
		if op.Operation == "stores_search" {
			// Market IDs are public store identifiers, not account data — printed
			// here (unlike every other operation's result) so the next diagnostic
			// round can pass a real one to timeslots_list/order operations.
			fmt.Println(describeStoresSearchValues(opResult))
		}
	}

	// Second diagnostic pass: call the validator directly (a fresh Do()
	// round trip) so a parse failure reports its typed shopping error and
	// Problem enum, rather than Accept()'s further-collapsed
	// "validation_failed". These error types never carry response content —
	// Operation is a fixed literal and Problem is a closed enum.
	if _, err := (rewe.BrowserValidator{Transport: server}).ValidateSession(actionContext); err != nil {
		fmt.Printf("session_identity validation failed: %s\n", describeShoppingError(err))
	} else {
		fmt.Println("session_identity validation succeeded on its own second pass.")
	}

	// State-machine pass: reports rather than aborts on Accept()'s error, and
	// times the call, so a logged-out session's outcome is fully visible —
	// does it promptly reach ReauthRequired (correct), some other state
	// (a misclassification), or not return until the -timeout deadline (a
	// hang)? Never fatal: a card #10 logged-out test round is expected to
	// end here with ReauthRequired, not an error exit.
	service := auth.NewService(rewe.BrowserValidator{Transport: server})
	service.Connect()
	acceptStart := time.Now()
	acceptErr := service.Accept(actionContext, browserbridge.NewTabBinding(time.Now()))
	acceptElapsed := time.Since(acceptStart)
	if acceptErr != nil {
		var coded browserbridge.CodedError
		if errors.As(acceptErr, &coded) {
			fmt.Printf("browser proof rejected (%s) after %s\n", coded.Code(), acceptElapsed)
		} else {
			fmt.Printf("browser proof rejected (%v) after %s\n", acceptErr, acceptElapsed)
		}
	}
	fmt.Printf("auth state after Accept(): %s (took %s)\n", service.Status().State, acceptElapsed)
	if service.Status().State == auth.StateActive {
		fmt.Println("session_identity accepted by the read-only REWE check.")
	}
	service.Disconnect()
	return nil
}

// describeJSONShape prints object keys, array lengths, and scalar types —
// never values — so a real upstream response can be diagnosed against this
// repo's no-raw-payload rule.
func describeJSONShape(data []byte) string {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Sprintf("<invalid JSON: %v>", err)
	}
	var b strings.Builder
	writeJSONShape(&b, value, 0, 9)
	return b.String()
}

func writeJSONShape(b *strings.Builder, value any, depth, maxDepth int) {
	indent := strings.Repeat("  ", depth)
	switch v := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(b, "%sobject{%d keys}\n", indent, len(keys))
		if depth >= maxDepth {
			return
		}
		for _, k := range keys {
			fmt.Fprintf(b, "%s  %q:\n", indent, k)
			writeJSONShape(b, v[k], depth+2, maxDepth)
		}
	case []any:
		fmt.Fprintf(b, "%sarray[%d]\n", indent, len(v))
		if len(v) > 0 && depth < maxDepth {
			writeJSONShape(b, v[0], depth+1, maxDepth)
		}
	case string:
		fmt.Fprintf(b, "%sstring\n", indent)
	case float64:
		fmt.Fprintf(b, "%snumber\n", indent)
	case bool:
		fmt.Fprintf(b, "%sbool\n", indent)
	case nil:
		fmt.Fprintf(b, "%snull\n", indent)
	default:
		fmt.Fprintf(b, "%s%T\n", indent, v)
	}
}

// describeStoresSearchValues prints the market_id/postal_code pairs a
// stores_search call actually returned. Unlike every other operation's
// result, these are public store identifiers, not account data, so this is
// the one diagnostic that names real values instead of shape only.
func describeStoresSearchValues(data []byte) string {
	var payload struct {
		Stores []struct {
			MarketID   string `json:"market_id"`
			PostalCode string `json:"postal_code"`
		} `json:"stores"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Sprintf("stores_search values: <could not parse: %v>", err)
	}
	var b strings.Builder
	b.WriteString("stores_search values (market_id / postal_code):\n")
	for _, store := range payload.Stores {
		fmt.Fprintf(&b, "  %s / %s\n", store.MarketID, store.PostalCode)
	}
	return b.String()
}

// describeShoppingError names the typed shopping error and, for the two
// classification types, its closed enum field — never response content.
func describeShoppingError(err error) string {
	var authErr *shopping.AuthError
	var bridgeErr *shopping.BridgeUnavailableError
	var rateErr *shopping.RateLimitError
	var validationErr *shopping.ValidationError
	var upstreamErr *shopping.UpstreamChangeError
	var ambiguousErr *shopping.AmbiguousResultError
	switch {
	case errors.As(err, &authErr):
		return "AuthError"
	case errors.As(err, &bridgeErr):
		return "BridgeUnavailableError"
	case errors.As(err, &rateErr):
		return "RateLimitError"
	case errors.As(err, &validationErr):
		return fmt.Sprintf("ValidationError(field=%s, problem=%s)", validationErr.Field, validationErr.Problem)
	case errors.As(err, &upstreamErr):
		return fmt.Sprintf("UpstreamChangeError(problem=%s)", upstreamErr.Problem)
	case errors.As(err, &ambiguousErr):
		return "AmbiguousResultError"
	default:
		return "unclassified error (not a shopping.* type)"
	}
}
