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

// ObserveWindowedSmall runs windowed observation with 4-turn windows
// (with assistant text) to test whether the owner-only advantage is
// about context size or about stripping assistant text.
func ObserveWindowedSmall(ctx context.Context, client inference.Client, conv *conversation.Conversation) (string, []Observation, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return "", nil, inference.Usage{}, nil
	}

	smallSize := 4
	smallStride := 2
	windows := buildWindows(turns, smallSize, smallStride)
	fmt.Fprintf(os.Stderr, "    windowed-small: %d turns → %d windows (size %d, stride %d)\n",
		len(turns), len(windows), smallSize, smallStride)

	observePrompt := prompts.Observe
	if isHumanSource(conv.Source) {
		observePrompt = prompts.ObserveHuman
	}

	var totalUsage inference.Usage
	var allCandidates []string

	for i, w := range windows {
		chunk := compressConversation(w, conv.Source, ContextDefault)
		input := strings.Join(chunk, "\n")
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

// ObserveWindowedOwnerOnlyLarge runs windowed owner-only with 16-turn
// windows to match context size of 8-turn with-assistant.
func ObserveWindowedOwnerOnlyLarge(ctx context.Context, client inference.Client, conv *conversation.Conversation) (string, []Observation, inference.Usage, error) {
	turns := extractTurns(conv)
	if len(turns) == 0 {
		return "", nil, inference.Usage{}, nil
	}

	largeSize := 16
	largeStride := 8
	windows := buildWindows(turns, largeSize, largeStride)
	fmt.Fprintf(os.Stderr, "    windowed-owner-only-large: %d turns → %d windows (size %d, stride %d)\n",
		len(turns), len(windows), largeSize, largeStride)

	observePrompt := prompts.Observe
	if isHumanSource(conv.Source) {
		observePrompt = prompts.ObserveHuman
	}

	var totalUsage inference.Usage
	var allCandidates []string

	for i, w := range windows {
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
