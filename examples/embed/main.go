// Example: embedding gori as a library with a custom tool.
//
// Run with: ANTHROPIC_API_KEY=... go run ./examples/embed
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/provider/anthropic"
)

func main() {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		fmt.Println("set ANTHROPIC_API_KEY to run this example")
		return
	}

	tools := gori.NewRegistry()
	tools.Register(gori.ToolFunc{
		NameVal:        "add",
		DescriptionVal: "add two integers a and b",
		SchemaVal:      json.RawMessage(`{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":"integer"}},"required":["a","b"]}`),
		Fn: func(_ context.Context, in json.RawMessage) (string, error) {
			var args struct{ A, B int }
			if err := json.Unmarshal(in, &args); err != nil {
				return "", err
			}
			return fmt.Sprintf("%d", args.A+args.B), nil
		},
	})

	agent := &gori.Agent{
		Provider: anthropic.New(key),
		Model:    "claude-sonnet-4-6",
		System:   "You are a helpful assistant. Use tools when needed.",
		Tools:    tools,
		Session:  gori.NewSession(),
	}

	out, err := agent.Run(context.Background(), "What is 21 + 21? Use the add tool.")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(out.Text())
}
