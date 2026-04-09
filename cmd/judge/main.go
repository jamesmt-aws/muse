// Judge: uses Opus to deduplicate and compare observation sets from
// different strategies on the same conversation.
//
// Usage: go run ./cmd/judge <observation-files...>
//
// Each file should be a text file with one strategy's observations,
// named like "baseline.txt", "windowed.txt", etc.
// The judge produces a canonical list of distinct insights and maps
// which strategies found each one.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/inference"
)

const judgePrompt = `You are judging the output of four observation strategies run on the same conversation. Each strategy tried to extract observations about how a person thinks from the same conversation.

Your job:

1. Read all four observation sets.
2. Deduplicate across all four into a canonical list of DISTINCT insights. Two observations are the same insight if they describe the same reasoning pattern, stance, or behavior, even if worded differently.
3. For each distinct insight, note which strategies found it (by name).
4. At the end, produce a summary table.

Output format:

For each distinct insight:

Insight: [one-sentence description of the distinct insight]
Found by: [comma-separated strategy names]
Example: [the best-worded version from any strategy that found it]

Then a summary:

SUMMARY
Total distinct insights: [N]
[Strategy name]: [N] insights ([N] unique to this strategy)
...

Be thorough. Don't merge insights that are genuinely different just because they share a topic. Two observations about "naming" that capture different stances (e.g., "prefers short names" vs "names should describe function not role") are distinct insights.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: judge <observation-file> [...]\n")
		os.Exit(1)
	}
	ctx := context.Background()

	// Use Opus for judging
	client, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bedrock client: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Model: %s\n\n", client.Model())

	var input strings.Builder
	for _, path := range os.Args[1:] {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			continue
		}
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		fmt.Fprintf(&input, "=== STRATEGY: %s ===\n\n%s\n\n", name, string(data))
	}

	fmt.Fprintf(os.Stderr, "Sending %d chars to judge...\n", input.Len())

	resp, usage, err := inference.Converse(ctx, client, judgePrompt, input.String(),
		inference.WithMaxTokens(8192))
	if err != nil {
		fmt.Fprintf(os.Stderr, "judge error: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "Tokens: %dk in / %dk out\n", usage.InputTokens/1000, usage.OutputTokens/1000)

	fmt.Println(resp)
}
