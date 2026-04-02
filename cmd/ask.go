package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ellistarn/muse/internal/inference"
	"github.com/ellistarn/muse/internal/muse"
	"github.com/ellistarn/muse/internal/storage"
)

func newAskCmd() *cobra.Command {
	var peer string
	var project string

	cmd := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your muse a question",
		Long: `Sends a question to your muse and streams the response. Each call is
stateless — your muse has no recall of previous questions. Ask opinionated
questions ("Is X a good approach for Y?") rather than factual lookups.

Use --peer to ask a peer's muse instead of your own:
  muse ask --peer github/ellistarn --project karpenter "Review this PR"`,
		Example: `  muse ask "Is a monorepo the right call for this project?"
  muse ask "How should I structure error handling in Go?"
  muse ask --peer github/ellistarn --project karpenter "Review this design"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var document string
			if peer != "" {
				doc, err := loadPeerDocument(peer, project)
				if err != nil {
					return err
				}
				document = doc
			} else {
				store, err := newStore(ctx)
				if err != nil {
					return err
				}
				document = loadDocument(ctx, store)
			}

			llm, err := newLLMClient(ctx, TierStrong)
			if err != nil {
				return err
			}
			m := muse.New(llm, document)
			question := strings.Join(args, " ")
			var wroteOutput bool
			_, err = m.Ask(ctx, muse.AskInput{
				Question: question,
				StreamFunc: inference.StreamFunc(func(delta inference.StreamDelta) {
					fmt.Fprint(os.Stdout, delta.Text)
					wroteOutput = true
				}),
			})
			if wroteOutput {
				fmt.Fprintln(os.Stdout) // trailing newline after stream completes
			}
			return err
		},
	}
	cmd.Flags().StringVar(&peer, "peer", "", "ask a peer's muse (e.g. github/ellistarn)")
	cmd.Flags().StringVar(&project, "project", "", "project scope for the peer muse")
	return cmd
}

// loadPeerDocument loads a peer's muse.md from the peer store.
func loadPeerDocument(peer, project string) (string, error) {
	parts := strings.SplitN(peer, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("invalid peer format %q (use source/username, e.g. github/ellistarn)", peer)
	}
	source, username := parts[0], parts[1]
	if source != "github" {
		return "", fmt.Errorf("unsupported peer source %q (only github is supported)", source)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	peerDir := fmt.Sprintf("github-%s", username)
	if project != "" {
		peerDir = fmt.Sprintf("github-%s/%s", username, project)
	}
	peerRoot := filepath.Join(home, ".muse", "peers", peerDir)
	store := storage.NewLocalStoreWithRoot(peerRoot)

	doc, err := store.GetMuse(context.Background())
	if err != nil {
		return "", fmt.Errorf("no muse found for peer %s (run: muse compose %s/%s)", peer, source, username)
	}
	return doc, nil
}
