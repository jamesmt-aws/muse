// compose-mini: takes observations and produces a mini muse document.
// This is not the full muse pipeline. It just asks Opus to synthesize
// observations into a coherent description of how someone thinks.
//
// Usage: go run ./cmd/compose-mini <observations.txt>
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/inference"
)

const composePrompt = `You are writing a muse — a concise description of how a specific person thinks, based on observations extracted from their conversations.

Each observation describes a reasoning pattern, stance, or behavior. Your job is to synthesize these into a coherent, readable portrait. Group related observations. Use the person's own words (from Quote lines) when they're vivid. Cut redundancy. The output should read like a description written by someone who knows this person well.

Write in third person. Keep it under 2000 words. Organize by theme, not by observation order.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: compose-mini <observations.txt>\n")
		os.Exit(1)
	}
	ctx := context.Background()

	client, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bedrock client: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Model: %s\n\n", client.Model())

	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read: %v\n", err)
		os.Exit(1)
	}

	resp, usage, err := inference.Converse(ctx, client, composePrompt, string(data),
		inference.WithMaxTokens(4096))
	if err != nil {
		fmt.Fprintf(os.Stderr, "compose error: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "Tokens: %dk in / %dk out\n", usage.InputTokens/1000, usage.OutputTokens/1000)
	fmt.Println(resp)
}
