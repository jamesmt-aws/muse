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
	var newSession bool
	var peer string
	var project string

	cmd := &cobra.Command{
		Use:   "ask [question]",
		Short: "Ask your muse a question",
		Long: `Sends a question to your muse and streams the response. By default, continues
the most recent conversation so your muse remembers prior context. Use --new
to start a fresh session.

Use --peer to ask a peer's muse instead of your own:
  muse ask --peer github/ellistarn --project karpenter "Review this PR"`,
		Example: `  muse ask "Is a monorepo the right call for this project?"
  muse ask "Tell me more about that"
  muse ask --new "How should I structure error handling in Go?"
  muse ask --peer github/ellistarn --project karpenter "Review this design"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			var document string
			var sessionsDir string

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			if peer != "" {
				doc, err := loadPeerDocument(peer, project)
				if err != nil {
					return err
				}
				document = doc
				sessionsDir = filepath.Join(home, ".muse", "peers", peerDir(peer, project), "sessions")
			} else {
				store, err := newStore(ctx)
				if err != nil {
					return err
				}
				document = loadDocument(ctx, store)
				sessionsDir = filepath.Join(home, ".muse", "sessions")
			}

			llm, err := newLLMClient(ctx, TierStrong)
			if err != nil {
				return err
			}

			m := muse.New(llm, document, muse.WithSessionsDir(sessionsDir))

			question := strings.Join(args, " ")
			result, err := m.Ask(ctx, muse.AskInput{
				Question: question,
				New:      newSession,
				StreamFunc: inference.StreamFunc(func(delta inference.StreamDelta) {
					fmt.Fprint(os.Stdout, delta.Text)
				}),
			})
			if result != nil && result.Response != "" {
				fmt.Fprintln(os.Stdout)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&newSession, "new", false, "start a fresh session instead of continuing the last one")
	cmd.Flags().StringVar(&peer, "peer", "", "ask a peer's muse (e.g. github/ellistarn)")
	cmd.Flags().StringVar(&project, "project", "", "project scope for the peer muse")
	return cmd
}

func peerDir(peer, project string) string {
	parts := strings.SplitN(peer, "/", 2)
	dir := fmt.Sprintf("github-%s", parts[1])
	if project != "" {
		dir = fmt.Sprintf("github-%s/%s", parts[1], project)
	}
	return dir
}

func loadPeerDocument(peer, project string) (string, error) {
	parts := strings.SplitN(peer, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", fmt.Errorf("invalid peer format %q (use source/username, e.g. github/ellistarn)", peer)
	}
	if parts[0] != "github" {
		return "", fmt.Errorf("unsupported peer source %q (only github is supported)", parts[0])
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	peerRoot := filepath.Join(home, ".muse", "peers", peerDir(peer, project))
	store := storage.NewLocalStoreWithRoot(peerRoot)

	doc, err := store.GetMuse(context.Background())
	if err != nil {
		return "", fmt.Errorf("no muse found for peer %s (run: muse compose %s)", peer, peer)
	}
	return doc, nil
}
