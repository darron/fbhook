// Example application-owned feedback integration. FBHOOK_ENDPOINT is a private
// server-side environment variable; provider credentials are never required here.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/darron/fbhook"
	"os"
	"time"
)

func main() {
	client, e := fbhook.NewClient(os.Getenv("FBHOOK_ENDPOINT"), nil)
	if e != nil {
		fmt.Fprintln(os.Stderr, "set FBHOOK_ENDPOINT")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	f := fbhook.Prepare(fbhook.Feedback{Source: fbhook.Source{App: "example", Environment: "development"}}, fbhook.Feedback{Feedback: "Returning to results loses the selected filters.", Source: fbhook.Source{Actor: "agent"}, Context: json.RawMessage(`{"page":"/search","filters":{"year":2026}}`)})
	receipt, e := client.Submit(ctx, f, "example-feedback-v1")
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	_ = json.NewEncoder(os.Stdout).Encode(receipt)
}
