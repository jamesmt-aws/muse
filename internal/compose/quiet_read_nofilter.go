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

// ObserveQuietReadNoFilter runs quiet read without the mechanical filter,
// letting triage handle all classification including short confirmations.
func ObserveQuietReadNoFilter(ctx context.Context, client inference.Client, conv *conversation.Conversation) (string, []Observation, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return "", nil, inference.Usage{}, nil
	}

	var totalUsage inference.Usage

	// Skip mechanical filter, go straight to triage
	triaged := turns
	if len(turns) > 10 {
		t, usage, err := triageTurns(ctx, client, turns, conv.Source, true)
		totalUsage = totalUsage.Add(usage)
		if err != nil {
			fmt.Fprintf(os.Stderr, "    triage error, falling back: %v\n", err)
		} else if len(t) > 0 {
			triaged = t
		}
	}
	if len(triaged) == 0 {
		return "", nil, totalUsage, nil
	}

	// Owner-only observe
	var b strings.Builder
	for _, t := range triaged {
		fmt.Fprintf(&b, "[owner]: %s\n\n", t.humanContent)
	}
	observeInput := b.String()
	fmt.Fprintf(os.Stderr, "    owner-only %d turns, %d chars\n", len(triaged), len(observeInput))

	observePrompt := prompts.Observe
	if isHumanSource(conv.Source) {
		observePrompt = prompts.ObserveHuman
	}

	start := time.Now()
	obs, usage, err := inference.Converse(ctx, client, observePrompt, observeInput, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	fmt.Fprintf(os.Stderr, "      observe %d chars → %d chars (%s)\n",
		len(observeInput), len(obs), time.Since(start).Round(time.Millisecond))
	if err != nil && obs == "" {
		return "", nil, totalUsage, err
	}
	if isEmpty(obs) {
		return "", nil, totalUsage, nil
	}

	items := parseObservationItems(obs)
	var relevant []Observation
	for _, item := range items {
		if isRelevant(item.Text) {
			relevant = append(relevant, item)
		}
	}
	return obs, relevant, totalUsage, nil
}
