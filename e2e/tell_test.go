package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

func TestTellStoresSession(t *testing.T) {
	store := storage.NewLocalStoreWithRoot(t.TempDir())
	ctx := context.Background()

	// Simulate what the tell command does: create a session with source "muse"
	now := mustParseTime(t, "20250615T143022Z")
	sessionID := "20250615T143022Z"
	message := "I prefer table-driven tests over sequential assertions"

	session := &conversation.Session{
		SchemaVersion: 1,
		Source:        "muse",
		SessionID:     sessionID,
		Title:         message,
		CreatedAt:     now,
		UpdatedAt:     now,
		Messages: []conversation.Message{
			{
				Role:      "user",
				Content:   message,
				Timestamp: now,
			},
		},
	}

	n, err := store.PutSession(ctx, session)
	if err != nil {
		t.Fatalf("PutSession() error: %v", err)
	}
	if n == 0 {
		t.Error("expected non-zero bytes written")
	}

	// Verify it can be read back
	got, err := store.GetSession(ctx, "muse", sessionID)
	if err != nil {
		t.Fatalf("GetSession() error: %v", err)
	}
	if got.Source != "muse" {
		t.Errorf("source = %q, want %q", got.Source, "muse")
	}
	if got.SessionID != sessionID {
		t.Errorf("sessionID = %q, want %q", got.SessionID, sessionID)
	}
	if got.Title != message {
		t.Errorf("title = %q, want %q", got.Title, message)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
	if got.Messages[0].Role != "user" {
		t.Errorf("role = %q, want %q", got.Messages[0].Role, "user")
	}
	if got.Messages[0].Content != message {
		t.Errorf("content = %q, want %q", got.Messages[0].Content, message)
	}

	// Verify it appears in ListSessions
	entries, err := store.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions() error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Source != "muse" {
		t.Errorf("entry source = %q, want %q", entries[0].Source, "muse")
	}
	if entries[0].Key != "conversations/muse/20250615T143022Z.json" {
		t.Errorf("entry key = %q, want %q", entries[0].Key, "conversations/muse/20250615T143022Z.json")
	}
}

func mustParseTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("20060102T150405Z", s)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", s, err)
	}
	return v
}
