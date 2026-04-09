package compose

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/prompts"
)

// LabeledObservation is an observation with a source label.
type LabeledObservation struct {
	Source      string // "REASONING" or "INTERACTION"
	Quote       string
	Text        string
}

// ObserveWindowedLabeled runs sliding-window observation using the labeled
// observe prompt, which tags each observation as REASONING or INTERACTION.
// Returns all observations with their labels.
func ObserveWindowedLabeled(ctx context.Context, client inference.Client, conv *conversation.Conversation) ([]LabeledObservation, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return nil, inference.Usage{}, nil
	}

	windows := buildWindows(turns, windowSize, windowStride)
	fmt.Fprintf(os.Stderr, "    labeled-windowed: %d turns → %d windows (size %d, stride %d)\n",
		len(turns), len(windows), windowSize, windowStride)

	var totalUsage inference.Usage
	var allRaw []string

	for i, w := range windows {
		chunk := compressConversation(w, conv.Source, ContextDefault)
		input := strings.Join(chunk, "\n")
		start := time.Now()
		obs, usage, err := inference.Converse(ctx, client, prompts.ObserveLabeled, input, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(usage)
		fmt.Fprintf(os.Stderr, "      window[%d/%d] %d turns, %d chars → %d chars (%s)\n",
			i+1, len(windows), len(w), len(input), len(obs), time.Since(start).Round(time.Millisecond))
		if err != nil && obs == "" {
			return nil, totalUsage, err
		}
		if obs != "" && !isEmpty(obs) {
			allRaw = append(allRaw, obs)
		}
	}

	if len(allRaw) == 0 {
		return nil, totalUsage, nil
	}

	// Parse all observations from all windows, then deduplicate by text.
	// Skip the refine step because it strips Source labels.
	var all []LabeledObservation
	for _, raw := range allRaw {
		all = append(all, parseLabeledObservations(raw)...)
	}

	// Simple dedup: keep first occurrence of each observation text
	seen := map[string]bool{}
	var deduped []LabeledObservation
	for _, o := range all {
		key := strings.ToLower(strings.TrimSpace(o.Text))
		if !seen[key] {
			seen[key] = true
			deduped = append(deduped, o)
		}
	}

	fmt.Fprintf(os.Stderr, "      %d raw → %d deduplicated\n", len(all), len(deduped))
	return deduped, totalUsage, nil
}

// parseLabeledObservations parses the labeled output format:
//   Source: REASONING
//   Quote: "..."
//   Observation: ...
func parseLabeledObservations(raw string) []LabeledObservation {
	var obs []LabeledObservation
	lines := strings.Split(raw, "\n")

	var current LabeledObservation
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Source:"):
			// Start of a new observation group - save previous if complete
			if current.Text != "" {
				obs = append(obs, current)
			}
			current = LabeledObservation{
				Source: strings.TrimSpace(strings.TrimPrefix(trimmed, "Source:")),
			}
		case strings.HasPrefix(trimmed, "Quote:"):
			current.Quote = strings.TrimSpace(strings.TrimPrefix(trimmed, "Quote:"))
		case strings.HasPrefix(trimmed, "Observation:"):
			current.Text = strings.TrimSpace(strings.TrimPrefix(trimmed, "Observation:"))
		}
	}
	if current.Text != "" {
		obs = append(obs, current)
	}
	return obs
}
