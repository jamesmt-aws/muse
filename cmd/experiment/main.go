// Observation strategy experiment: runs baseline, windowed, and quiet read
// on conversations or essays using Haiku.
//
// Usage:
//   go run ./cmd/experiment <conversation.json> [...]
//   go run ./cmd/experiment --essay <text-file> [...]
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ellistarn/muse/internal/bedrock"
	"github.com/ellistarn/muse/internal/compose"
	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/prompts"
)

func main() {
	essayMode := false
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--essay" {
		essayMode = true
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: experiment [--essay] <file> [...]\n")
		os.Exit(1)
	}
	ctx := context.Background()

	os.Setenv("MUSE_MODEL", "us.anthropic.claude-haiku-4-5-20251001-v1:0")
	client, err := bedrock.NewClient(ctx, "claude-haiku")
	if err != nil {
		fmt.Fprintf(os.Stderr, "bedrock client: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Model: %s\n\n", client.Model())

	if essayMode {
		runEssayExperiment(ctx, client, args)
	} else {
		runConversationExperiment(ctx, client, args)
	}
}

// runEssayExperiment makes direct observe calls on essay text,
// bypassing the pipeline's turn extraction and threshold logic.
func runEssayExperiment(ctx context.Context, client inference.Client, paths []string) {
	for _, path := range paths {
		text, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			continue
		}
		essay := string(text)

		fmt.Printf("###########################################################################\n")
		fmt.Printf("ESSAY: %s (%d chars)\n", filepath.Base(path), len(essay))
		fmt.Printf("###########################################################################\n")

		// Baseline: full essay to essay-mode observe prompt
		fmt.Printf("\n--- Strategy 1: BASELINE (full essay) ---\n")
		raw, usage, err := inference.Converse(ctx, client, prompts.ObserveEssay, essay)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  baseline error: %v\n", err)
		}
		printObserveResult(raw, usage)

		// Quiet read: triage paragraphs, then observe only reasoning paragraphs
		fmt.Printf("\n--- Strategy 2: QUIET READ (reasoning paragraphs only) ---\n")
		paragraphs := splitParagraphs(essay)
		fmt.Printf("  Paragraphs: %d\n", len(paragraphs))

		// Triage: ask which paragraphs contain reasoning
		var numbered strings.Builder
		for i, p := range paragraphs {
			fmt.Fprintf(&numbered, "Paragraph %d:\n%s\n\n", i+1, p)
		}
		triagePrompt := "Classify each numbered paragraph as REASONING or SKIP.\n\n" +
			"REASONING: the author takes a position, explains why, draws a conclusion, " +
			"makes an argument, models their own thinking, or prescribes a method.\n\n" +
			"SKIP: specimens of bad writing, block quotes, lists of examples, " +
			"catalog entries, or passages that demonstrate rather than argue.\n\n" +
			"Return a JSON array of paragraph numbers classified as REASONING. " +
			"Example: [1, 3, 7, 12]"
		triageRaw, _, err := inference.Converse(ctx, client, triagePrompt, numbered.String())
		if err != nil {
			fmt.Fprintf(os.Stderr, "  triage error: %v\n", err)
		}
		fmt.Printf("  Triage: %s\n", strings.TrimSpace(triageRaw))

		// Parse triage response to get reasoning paragraph indices
		reasoningIdxs := parseTriageResponse(triageRaw, len(paragraphs))
		var reasoning []string
		for _, idx := range reasoningIdxs {
			reasoning = append(reasoning, paragraphs[idx-1])
		}
		fmt.Printf("  Reasoning paragraphs: %d of %d\n", len(reasoning), len(paragraphs))

		if len(reasoning) == 0 {
			fmt.Printf("  Result: NONE (triage found no reasoning)\n")
		} else {
			quietInput := strings.Join(reasoning, "\n\n")
			raw2, usage2, err := inference.Converse(ctx, client, prompts.ObserveEssay, quietInput)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  quiet read error: %v\n", err)
			}
			printObserveResult(raw2, usage2)
		}
	}
}

func printObserveResult(raw string, usage inference.Usage) {
	trimmed := strings.TrimSpace(raw)
	obs := countObservations(trimmed)
	fmt.Printf("  Observations: %d\n", obs)
	fmt.Printf("  Tokens: %dk in / %dk out\n", usage.InputTokens/1000, usage.OutputTokens/1000)
	if trimmed == "" || trimmed == "NONE" {
		fmt.Printf("  Result: NONE\n")
	} else {
		fmt.Printf("\n%s\n", trimmed)
	}
}

func countObservations(raw string) int {
	count := 0
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Observation:") {
			count++
		}
	}
	return count
}

// splitParagraphs splits text on blank lines, dropping empty results.
func splitParagraphs(text string) []string {
	raw := strings.Split(text, "\n\n")
	var out []string
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseTriageResponse extracts paragraph numbers from a JSON array in the response.
func parseTriageResponse(raw string, maxParagraphs int) []int {
	// Find the JSON array in the response
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start < 0 || end < 0 || end <= start {
		return nil
	}
	var nums []int
	if err := json.Unmarshal([]byte(raw[start:end+1]), &nums); err != nil {
		return nil
	}
	// Filter to valid range
	var valid []int
	for _, n := range nums {
		if n >= 1 && n <= maxParagraphs {
			valid = append(valid, n)
		}
	}
	return valid
}

// runConversationExperiment uses the full pipeline on conversation JSON files.
func runConversationExperiment(ctx context.Context, client inference.Client, paths []string) {
	strategies := []struct {
		name string
		mode compose.ObserveMode
	}{
		{"BASELINE", compose.ObserveFullConversation},
		{"WINDOWED", compose.ObserveWindowed},
		{"QUIET READ", compose.ObserveTriageOwnerOnly},
	}

	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
			continue
		}
		var conv conversation.Conversation
		if err := json.Unmarshal(data, &conv); err != nil {
			fmt.Fprintf(os.Stderr, "parse %s: %v\n", path, err)
			continue
		}

		userMsgs := 0
		for _, m := range conv.Messages {
			if m.Role == "user" {
				userMsgs++
			}
		}

		fmt.Printf("\n###########################################################################\n")
		fmt.Printf("CONVERSATION: %s — %d messages (%d user turns)\n",
			filepath.Base(path), len(conv.Messages), userMsgs)
		fmt.Printf("###########################################################################\n")

		for i, s := range strategies {
			fmt.Printf("\n--- Strategy %d: %s ---\n", i+1, s.name)

			raw, obs, usage, err := compose.ObserveForTestVerbose(ctx, client, &conv, compose.ContextDefault, s.mode)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s error: %v\n", s.name, err)
			}
			fmt.Printf("  Observations: %d\n", len(obs))
			fmt.Printf("  Tokens: %dk in / %dk out\n", usage.InputTokens/1000, usage.OutputTokens/1000)

			if raw == "" || strings.TrimSpace(raw) == "NONE" {
				fmt.Printf("  Result: NONE\n")
			} else {
				fmt.Printf("\n%s\n", raw)
			}
		}

		// Strategy 4: Combined (windowed with labeled observations)
		fmt.Printf("\n--- Strategy 4: COMBINED (windowed + labeled) ---\n")
		labeled, labeledUsage, err := compose.ObserveWindowedLabeled(ctx, client, &conv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  COMBINED error: %v\n", err)
		}
		var reasoning, interaction []compose.LabeledObservation
		for _, o := range labeled {
			switch o.Source {
			case "REASONING":
				reasoning = append(reasoning, o)
			case "INTERACTION":
				interaction = append(interaction, o)
			default:
				reasoning = append(reasoning, o)
			}
		}
		fmt.Printf("  Total: %d  REASONING: %d  INTERACTION: %d\n",
			len(labeled), len(reasoning), len(interaction))
		fmt.Printf("  Tokens: %dk in / %dk out\n",
			labeledUsage.InputTokens/1000, labeledUsage.OutputTokens/1000)

		if len(labeled) > 0 {
			fmt.Printf("\n  === REASONING ===\n")
			for _, o := range reasoning {
				if o.Quote != "" {
					fmt.Printf("  Quote: %s\n", o.Quote)
				}
				fmt.Printf("  Observation: %s\n\n", o.Text)
			}
			if len(interaction) > 0 {
				fmt.Printf("  === INTERACTION ===\n")
				for _, o := range interaction {
					if o.Quote != "" {
						fmt.Printf("  Quote: %s\n", o.Quote)
					}
					fmt.Printf("  Observation: %s\n\n", o.Text)
				}
			}
		} else {
			fmt.Printf("  Result: NONE\n")
		}

		// Strategy 5: Windowed owner-only (no triage, no filter)
		fmt.Printf("\n--- Strategy 5: WINDOWED OWNER-ONLY ---\n")
		raw5, obs5, usage5, err := compose.ObserveWindowedOwnerOnly(ctx, client, &conv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  WINDOWED OWNER-ONLY error: %v\n", err)
		}
		fmt.Printf("  Observations: %d\n", len(obs5))
		fmt.Printf("  Tokens: %dk in / %dk out\n", usage5.InputTokens/1000, usage5.OutputTokens/1000)
		if raw5 == "" || strings.TrimSpace(raw5) == "NONE" {
			fmt.Printf("  Result: NONE\n")
		} else {
			fmt.Printf("\n%s\n", raw5)
		}
	}
}
