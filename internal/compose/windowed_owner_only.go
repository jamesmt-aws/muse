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

// ObserveWindowedOwnerOnly runs sliding-window observation with assistant
// text stripped from each window. Each window is small enough to avoid
// context rot, and owner-only framing focuses on reasoning.
func ObserveWindowedOwnerOnly(ctx context.Context, client inference.Client, conv *conversation.Conversation) (string, []Observation, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return "", nil, inference.Usage{}, nil
	}

	windows := buildWindows(turns, windowSize, windowStride)
	fmt.Fprintf(os.Stderr, "    windowed-owner-only: %d turns → %d windows (size %d, stride %d)\n",
		len(turns), len(windows), windowSize, windowStride)

	observePrompt := prompts.Observe
	if isHumanSource(conv.Source) {
		observePrompt = prompts.ObserveHuman
	}

	var totalUsage inference.Usage
	var allCandidates []string

	for i, w := range windows {
		// Build owner-only input for this window
		var b strings.Builder
		for _, t := range w {
			fmt.Fprintf(&b, "[owner]: %s\n\n", t.humanContent)
		}
		input := b.String()

		start := time.Now()
		obs, usage, err := inference.Converse(ctx, client, observePrompt, input, inference.WithMaxTokens(4096))
		totalUsage = totalUsage.Add(usage)
		fmt.Fprintf(os.Stderr, "      window[%d/%d] %d turns, %d chars → %d chars (%s)\n",
			i+1, len(windows), len(w), len(input), len(obs), time.Since(start).Round(time.Millisecond))
		if err != nil && obs == "" {
			return "", nil, totalUsage, err
		}
		if obs != "" && !isEmpty(obs) {
			allCandidates = append(allCandidates, obs)
		}
	}

	if len(allCandidates) == 0 {
		return "", nil, totalUsage, nil
	}

	candidates := deduplicateObservationText(strings.Join(allCandidates, "\n\n"))
	start := time.Now()
	refined, usage, err := inference.Converse(ctx, client, prompts.Refine, candidates, inference.WithMaxTokens(4096))
	totalUsage = totalUsage.Add(usage)
	fmt.Fprintf(os.Stderr, "      refine %d chars → %d chars (%s)\n",
		len(candidates), len(refined), time.Since(start).Round(time.Millisecond))
	if err != nil && refined == "" {
		return "", nil, totalUsage, err
	}
	if isEmpty(refined) {
		return "", nil, totalUsage, nil
	}

	items := parseObservationItems(refined)
	var relevant []Observation
	for _, item := range items {
		if isRelevant(item.Text) {
			relevant = append(relevant, item)
		}
	}
	return refined, relevant, totalUsage, nil
}
