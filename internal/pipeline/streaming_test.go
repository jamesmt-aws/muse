package pipeline

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

// mockProvider implements conversation.Provider for testing.
type mockProvider struct {
	name  string
	convs []conversation.Conversation
	err   error
	delay time.Duration // simulate slow source
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) Conversations(ctx context.Context, progress func(conversation.SyncProgress)) ([]conversation.Conversation, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.err != nil {
		return nil, m.err
	}
	return m.convs, nil
}

func makeConv(source, id string, updatedAt time.Time) conversation.Conversation {
	return conversation.Conversation{
		Source:         source,
		ConversationID: id,
		UpdatedAt:      updatedAt,
	}
}

func makeEntry(source, id string, lastModified time.Time) storage.ConversationEntry {
	return storage.ConversationEntry{
		Source:         source,
		ConversationID: id,
		Key:            fmt.Sprintf("conversations/%s/%s.json", source, id),
		LastModified:   lastModified,
	}
}

func TestRun_NoLimit(t *testing.T) {
	now := time.Now()
	convs := []conversation.Conversation{
		makeConv("local", "a", now.Add(-1*time.Hour)),
		makeConv("local", "b", now.Add(-2*time.Hour)),
		makeConv("local", "c", now.Add(-3*time.Hour)),
	}

	var observed []string
	var mu sync.Mutex

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  2,
		ObserveGoroutines: 2,
		ChannelBuffer:  4,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: convs},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			mu.Lock()
			observed = append(observed, entry.ConversationID)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Discovered != 3 {
		t.Errorf("Discovered = %d, want 3", result.Discovered)
	}
	if result.Observed != 3 {
		t.Errorf("Observed = %d, want 3", result.Observed)
	}
	if result.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0", result.Skipped)
	}
	if len(observed) != 3 {
		t.Errorf("len(observed) = %d, want 3", len(observed))
	}
}

func TestRun_WithSkips(t *testing.T) {
	now := time.Now()
	convs := []conversation.Conversation{
		makeConv("local", "a", now),
		makeConv("local", "b", now),
		makeConv("local", "c", now),
	}

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  2,
		ObserveGoroutines: 2,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: convs},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			// Only "a" needs observation.
			needs := conv.ConversationID == "a"
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), needs, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Observed != 1 {
		t.Errorf("Observed = %d, want 1", result.Observed)
	}
	if result.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", result.Skipped)
	}
}

func TestRun_LimitSelectsNewest(t *testing.T) {
	now := time.Now()
	convs := []conversation.Conversation{
		makeConv("local", "old", now.Add(-3*time.Hour)),
		makeConv("local", "mid", now.Add(-2*time.Hour)),
		makeConv("local", "new", now.Add(-1*time.Hour)),
	}

	var observed []string
	var mu sync.Mutex

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  1,
		ObserveGoroutines: 1,
		Limit:          2,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: convs},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			mu.Lock()
			observed = append(observed, entry.ConversationID)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Observed != 2 {
		t.Errorf("Observed = %d, want 2", result.Observed)
	}
	if result.Remaining != 1 {
		t.Errorf("Remaining = %d, want 1", result.Remaining)
	}

	// Should have observed "new" and "mid", not "old".
	seen := map[string]bool{}
	for _, id := range observed {
		seen[id] = true
	}
	if !seen["new"] || !seen["mid"] {
		t.Errorf("observed = %v, want {new, mid}", observed)
	}
	if seen["old"] {
		t.Errorf("observed includes 'old', should have been evicted by limit")
	}
}

func TestRun_MultipleSources(t *testing.T) {
	now := time.Now()

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  2,
		ObserveGoroutines: 2,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: []conversation.Conversation{
				makeConv("local", "a", now),
				makeConv("local", "b", now),
			}},
			&mockProvider{name: "github", convs: []conversation.Conversation{
				makeConv("github", "pr-1", now),
			}},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Discovered != 3 {
		t.Errorf("Discovered = %d, want 3", result.Discovered)
	}
	if result.SourceCounts["local"] != 2 {
		t.Errorf("SourceCounts[local] = %d, want 2", result.SourceCounts["local"])
	}
	if result.SourceCounts["github"] != 1 {
		t.Errorf("SourceCounts[github] = %d, want 1", result.SourceCounts["github"])
	}
}

func TestRun_ProviderError(t *testing.T) {
	now := time.Now()

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  2,
		ObserveGoroutines: 2,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: []conversation.Conversation{
				makeConv("local", "a", now),
			}},
			&mockProvider{name: "broken", err: fmt.Errorf("auth failed")},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Observed != 1 {
		t.Errorf("Observed = %d, want 1", result.Observed)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("len(Warnings) = %d, want 1", len(result.Warnings))
	}
}

func TestRun_ObserveError(t *testing.T) {
	now := time.Now()

	_, err := Run(context.Background(), Config{
		UploadGoroutines:  1,
		ObserveGoroutines: 1,
		Providers: []conversation.Provider{
			&mockProvider{name: "local", convs: []conversation.Conversation{
				makeConv("local", "a", now),
				makeConv("local", "b", now),
			}},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			return fmt.Errorf("llm failed")
		},
	})
	if err == nil {
		t.Fatal("Run() expected error, got nil")
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var observeCount atomic.Int32
	_, err := Run(ctx, Config{
		UploadGoroutines:  1,
		ObserveGoroutines: 1,
		Providers: []conversation.Provider{
			&mockProvider{name: "slow", convs: func() []conversation.Conversation {
				var out []conversation.Conversation
				for i := range 100 {
					out = append(out, makeConv("slow", fmt.Sprintf("c%d", i), time.Now()))
				}
				return out
			}()},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			n := observeCount.Add(1)
			if n >= 3 {
				cancel()
			}
			// Simulate work so goroutines check ctx.
			time.Sleep(time.Millisecond)
			return ctx.Err()
		},
	})
	// Pipeline should exit without hanging. Error may be context.Canceled or nil
	// depending on timing.
	_ = err

	if observeCount.Load() > 10 {
		t.Errorf("observed %d conversations after cancel, expected <=10", observeCount.Load())
	}
}

func TestRun_EmptyProviders(t *testing.T) {
	result, err := Run(context.Background(), Config{
		Providers: []conversation.Provider{},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			t.Fatal("Upload should not be called with no providers")
			return storage.ConversationEntry{}, false, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			t.Fatal("Observe should not be called with no providers")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Discovered != 0 {
		t.Errorf("Discovered = %d, want 0", result.Discovered)
	}
}

// ── Priority Queue Tests ──────────────────────────────────────────────

func TestPriorityQueue_InsertAndDrain(t *testing.T) {
	pq := newPriorityQueue(3)
	now := time.Now()

	pq.Insert(makeEntry("s", "b", now.Add(-2*time.Hour)))
	pq.Insert(makeEntry("s", "a", now.Add(-1*time.Hour)))
	pq.Insert(makeEntry("s", "c", now.Add(-3*time.Hour)))

	entries := pq.Drain()
	if len(entries) != 3 {
		t.Fatalf("Drain() returned %d entries, want 3", len(entries))
	}

	// Should be newest-first.
	if entries[0].ConversationID != "a" {
		t.Errorf("entries[0] = %s, want a", entries[0].ConversationID)
	}
	if entries[1].ConversationID != "b" {
		t.Errorf("entries[1] = %s, want b", entries[1].ConversationID)
	}
	if entries[2].ConversationID != "c" {
		t.Errorf("entries[2] = %s, want c", entries[2].ConversationID)
	}
}

func TestPriorityQueue_EvictsOldest(t *testing.T) {
	pq := newPriorityQueue(2)
	now := time.Now()

	pq.Insert(makeEntry("s", "old", now.Add(-3*time.Hour)))
	pq.Insert(makeEntry("s", "mid", now.Add(-2*time.Hour)))
	pq.Insert(makeEntry("s", "new", now.Add(-1*time.Hour)))

	entries := pq.Drain()
	if len(entries) != 2 {
		t.Fatalf("Drain() returned %d entries, want 2", len(entries))
	}

	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ConversationID] = true
	}
	if ids["old"] {
		t.Error("queue should have evicted 'old'")
	}
	if !ids["new"] || !ids["mid"] {
		t.Errorf("queue should contain new and mid, got %v", ids)
	}
}

func TestPriorityQueue_DiscardsOlderThanFull(t *testing.T) {
	pq := newPriorityQueue(2)
	now := time.Now()

	pq.Insert(makeEntry("s", "new", now.Add(-1*time.Hour)))
	pq.Insert(makeEntry("s", "mid", now.Add(-2*time.Hour)))
	// This is older than both, should be discarded.
	pq.Insert(makeEntry("s", "ancient", now.Add(-10*time.Hour)))

	entries := pq.Drain()
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ConversationID] = true
	}
	if ids["ancient"] {
		t.Error("'ancient' should have been discarded")
	}
}

// ── Watermark Tests ───────────────────────────────────────────────────

func TestWatermarks_CanFinalize(t *testing.T) {
	now := time.Now()
	wm := newWatermarks([]string{"local", "github"})

	// Neither source has produced anything — can't finalize.
	if wm.CanFinalize(now) {
		t.Error("should not finalize when no source has produced")
	}

	// Local produces something old.
	wm.Update("local", now.Add(-10*time.Hour))
	// GitHub hasn't produced anything — can't finalize.
	if wm.CanFinalize(now.Add(-5 * time.Hour)) {
		t.Error("should not finalize when github has no watermark")
	}

	// GitHub produces something old.
	wm.Update("github", now.Add(-8*time.Hour))
	// Oldest queue entry at -5h is newer than both watermarks (-10h, -8h).
	if !wm.CanFinalize(now.Add(-5 * time.Hour)) {
		t.Error("should finalize: -5h is newer than both watermarks")
	}

	// Oldest queue entry at -9h is NOT newer than local's -10h watermark,
	// but IS older than github's -8h — can't finalize.
	if wm.CanFinalize(now.Add(-9 * time.Hour)) {
		t.Error("should not finalize: -9h is older than github watermark -8h")
	}
}

func TestWatermarks_DoneSourceIgnored(t *testing.T) {
	now := time.Now()
	wm := newWatermarks([]string{"local", "github"})

	wm.Update("local", now.Add(-10*time.Hour))
	wm.MarkDone("local")
	wm.Update("github", now.Add(-8*time.Hour))

	// Local is done, so only github matters. -5h is newer than -8h.
	if !wm.CanFinalize(now.Add(-5 * time.Hour)) {
		t.Error("should finalize: local is done, -5h is newer than github's -8h")
	}
}

func TestRun_LimitWithEarlyTermination(t *testing.T) {
	now := time.Now()

	// "fast" source returns immediately with old conversations.
	// "slow" source takes 200ms and returns newer conversations.
	// With limit=2, the fast source's conversations should be evicted
	// by the slow source's newer ones.
	//
	// Use 1 upload goroutine to make processing order deterministic.
	var observed []string
	var mu sync.Mutex

	result, err := Run(context.Background(), Config{
		UploadGoroutines:  1,
		ObserveGoroutines: 1,
		Limit:             2,
		Providers: []conversation.Provider{
			&mockProvider{name: "fast", convs: []conversation.Conversation{
				makeConv("fast", "old1", now.Add(-10*time.Hour)),
				makeConv("fast", "old2", now.Add(-9*time.Hour)),
			}},
			&mockProvider{name: "slow", delay: 200 * time.Millisecond, convs: []conversation.Conversation{
				makeConv("slow", "new1", now.Add(-1*time.Hour)),
				makeConv("slow", "new2", now.Add(-2*time.Hour)),
			}},
		},
		Upload: func(ctx context.Context, conv *conversation.Conversation) (storage.ConversationEntry, bool, error) {
			return makeEntry(conv.Source, conv.ConversationID, conv.UpdatedAt), true, nil
		},
		Observe: func(ctx context.Context, entry storage.ConversationEntry) error {
			mu.Lock()
			observed = append(observed, entry.ConversationID)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	if result.Observed != 2 {
		t.Errorf("Observed = %d, want 2", result.Observed)
	}

	// The two newest should be from the slow source.
	seen := map[string]bool{}
	for _, id := range observed {
		seen[id] = true
	}
	if !seen["new1"] || !seen["new2"] {
		t.Errorf("observed = %v, want {new1, new2}", observed)
	}
}

func TestPriorityQueue_ConcurrentInsert(t *testing.T) {
	pq := newPriorityQueue(10)
	now := time.Now()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pq.Insert(makeEntry("s", fmt.Sprintf("c%d", i), now.Add(-time.Duration(i)*time.Minute)))
		}(i)
	}
	wg.Wait()

	entries := pq.Drain()
	if len(entries) != 10 {
		t.Fatalf("Drain() returned %d entries, want 10", len(entries))
	}

	// Should be the 10 newest (i=0..9, since Duration(i)*Minute means i=0 is newest).
	for _, e := range entries {
		// All should have timestamps within the first 10 minutes.
		age := now.Sub(e.LastModified)
		if age > 10*time.Minute {
			t.Errorf("entry %s is %v old, should be within 10m", e.ConversationID, age)
		}
	}
}
