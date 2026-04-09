// Quality judge: uses Opus to evaluate whether observations are
// well-grounded, misleading, or generic.
//
// Usage: go run ./cmd/judge-quality <observation-file> <source-text>
//
// The observation file contains observations from one strategy.
// The source text is the original conversation or essay that was observed.
// The judge evaluates each observation against the source material.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/inference"
)

const qualityPrompt = `You are evaluating the quality of observations extracted from a text. Each observation claims something about how the author thinks. Your job is to evaluate each one against the source material.

For each observation, assign one of three ratings:

GROUNDED — The observation is well-supported by the source text. The claimed reasoning pattern or stance is clearly visible in what the author wrote. A reader of the source text would agree this is a fair characterization.

GENERIC — The observation is technically true but could describe many people. It doesn't capture anything distinctive. "Values clarity" or "prefers concrete examples" are generic unless the specific stance is unusual.

MISLEADING — The observation overstates, mischaracterizes, or infers something the source text doesn't support. The author might disagree with this characterization of their thinking.

Output format:

For each observation, on one line:
[GROUNDED/GENERIC/MISLEADING] Observation text... | Reason: brief explanation

Then a summary:

SUMMARY
Total: [N]
Grounded: [N]
Generic: [N]
Misleading: [N]`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: judge-quality <observations.txt> <source-text>\n")
		os.Exit(1)
	}
	ctx := context.Background()

	client, err := bedrock.NewClient(ctx, "claude-opus")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bedrock client: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "Model: %s\n\n", client.Model())

	obsData, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read observations: %v\n", err)
		os.Exit(1)
	}

	sourceData, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "read source: %v\n", err)
		os.Exit(1)
	}

	var input strings.Builder
	fmt.Fprintf(&input, "=== SOURCE TEXT ===\n\n%s\n\n", string(sourceData))
	fmt.Fprintf(&input, "=== OBSERVATIONS TO EVALUATE ===\n\n%s\n", string(obsData))

	fmt.Fprintf(os.Stderr, "Sending %d chars to quality judge...\n", input.Len())

	resp, usage, err := inference.Converse(ctx, client, qualityPrompt, input.String(),
		inference.WithMaxTokens(8192))
	if err != nil {
		fmt.Fprintf(os.Stderr, "judge error: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "Tokens: %dk in / %dk out\n", usage.InputTokens/1000, usage.OutputTokens/1000)

	fmt.Println(resp)
}
