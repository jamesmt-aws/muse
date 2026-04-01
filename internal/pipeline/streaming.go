// Package pipeline implements streaming discover/observe orchestration.
// Sources push conversations through an upload channel into an observe channel,
// overlapping discovery, upload, and observation stages. See
// designs/discover-observe-streaming.md.
package pipeline

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ellistarn/muse/internal/conversation"
	"github.com/ellistarn/muse/internal/storage"
)

// ObserveFunc is called by observe goroutines for each conversation that needs
// observation. It receives the store entry metadata; the implementation is
// responsible for loading the conversation from the store. Errors cancel the
// pipeline.
type ObserveFunc func(ctx context.Context, entry storage.ConversationEntry) error

// UploadFunc is called by upload goroutines for each discovered conversation.
// It diffs against the store, uploads if changed, fingerprint-checks against
// existing observations, and returns whether the conversation needs observation.
type UploadFunc func(ctx context.Context, conv *conversation.Conversation) (entry storage.ConversationEntry, needsObserve bool, err error)

// Config controls the streaming pipeline.
type Config struct {
	// Goroutine counts per stage.
	UploadGoroutines  int // default 20
	ObserveGoroutines int // default 50

	// ChannelBuffer is the buffer size for both channels. Default 32.
	ChannelBuffer int

	// Limit caps how many conversations are observed. 0 = no limit.
	// When set, a priority queue selects the N newest conversations.
	Limit int

	// Providers are the conversation sources to discover from.
	Providers []conversation.Provider

	// SyncProgress receives progress updates from source providers.
	SyncProgress func(source string, p conversation.SyncProgress)

	// Upload is called for each discovered conversation to diff/upload/fingerprint.
	Upload UploadFunc

	// Observe is called for each conversation that needs observation.
	Observe ObserveFunc
}

func (c *Config) defaults() {
	if c.UploadGoroutines <= 0 {
		c.UploadGoroutines = 20
	}
	if c.ObserveGoroutines <= 0 {
		c.ObserveGoroutines = 50
	}
	if c.ChannelBuffer <= 0 {
		c.ChannelBuffer = 32
	}
}

// Result contains counters from a completed pipeline run.
type Result struct {
	Discovered   int            // total conversations found across all sources
	SourceCounts map[string]int // conversations per source
	Observed     int            // conversations sent through observe
	Skipped      int            // conversations skipped (fingerprint match)
	Remaining    int            // conversations that need observation but hit limit
	Warnings     []string       // non-fatal errors
}

// Run executes the streaming pipeline. It blocks until all stages complete or
// the context is cancelled.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	cfg.defaults()

	// Two context layers:
	// - discoverCtx: cancelled when watermarks finalize (limit path) or on error.
	//   Discover and upload goroutines use this context.
	// - observeCtx: cancelled only on error or external cancellation.
	//   Observe goroutines use this context so they keep running after
	//   discover/upload are cancelled by watermark finalization.
	observeCtx, observeCancel := context.WithCancel(ctx)
	defer observeCancel()
	discoverCtx, discoverCancel := context.WithCancel(observeCtx)
	defer discoverCancel()

	uploadCh := make(chan *conversation.Conversation, cfg.ChannelBuffer)
	observeCh := make(chan storage.ConversationEntry, cfg.ChannelBuffer)

	var result Result
	result.SourceCounts = make(map[string]int)
	var mu sync.Mutex

	addWarning := func(msg string) {
		mu.Lock()
		result.Warnings = append(result.Warnings, msg)
		mu.Unlock()
	}

	// ── WATERMARKS (limit path only) ──────────────────────────────────
	var pq *priorityQueue
	var wm *watermarks
	if cfg.Limit > 0 {
		pq = newPriorityQueue(cfg.Limit)
		names := make([]string, len(cfg.Providers))
		for i, p := range cfg.Providers {
			names[i] = p.Name()
		}
		wm = newWatermarks(names)
	}

	// ── DISCOVER ───────────────────────────────────────────────────────
	var discoverWg sync.WaitGroup
	for _, provider := range cfg.Providers {
		discoverWg.Add(1)
		go func(p conversation.Provider) {
			defer discoverWg.Done()
			name := p.Name()

			progressFn := func(conversation.SyncProgress) {}
			if cfg.SyncProgress != nil {
				progressFn = func(sp conversation.SyncProgress) {
					cfg.SyncProgress(name, sp)
				}
			}

			convs, err := p.Conversations(discoverCtx, progressFn)
			if cfg.SyncProgress != nil {
				cfg.SyncProgress(name, conversation.SyncProgress{
					Phase:  "done",
					Detail: fmt.Sprintf("%d conversations", len(convs)),
				})
			}
			if err != nil {
				// Context cancellation from watermark finalization is not a warning.
				if discoverCtx.Err() == nil {
					addWarning(fmt.Sprintf("failed to read %s conversations: %v", name, err))
				}
				if wm != nil {
					wm.MarkDone(name)
				}
				return
			}

			mu.Lock()
			result.SourceCounts[name] = len(convs)
			result.Discovered += len(convs)
			mu.Unlock()

			if wm != nil {
				wm.SetExpected(name, len(convs))
			}

			for i := range convs {
				select {
				case uploadCh <- &convs[i]:
				case <-discoverCtx.Done():
					return
				}
			}
		}(provider)
	}

	go func() {
		discoverWg.Wait()
		close(uploadCh)
	}()

	// ── UPLOAD ─────────────────────────────────────────────────────────
	var uploadWg sync.WaitGroup
	var needsObserveCount atomic.Int32
	var skipCount atomic.Int32

	for range cfg.UploadGoroutines {
		uploadWg.Add(1)
		go func() {
			defer uploadWg.Done()
			for conv := range uploadCh {
				if discoverCtx.Err() != nil {
					return
				}

				source := conv.Source
				entry, needsObserve, err := cfg.Upload(discoverCtx, conv)
				if err != nil {
					if discoverCtx.Err() == nil {
						addWarning(fmt.Sprintf("upload %s/%s: %v", conv.Source, conv.ConversationID, err))
					}
					if wm != nil {
						wm.RecordProcessed(source)
					}
					continue
				}

				if !needsObserve {
					skipCount.Add(1)
					if wm != nil {
						wm.RecordProcessed(source)
					}
					continue
				}

				needsObserveCount.Add(1)

				if pq != nil {
					pq.Insert(entry)
					wm.Update(source, entry.LastModified)
					wm.RecordProcessed(source)
				} else {
					select {
					case observeCh <- entry:
					case <-observeCtx.Done():
						return
					}
				}
			}
		}()
	}

	// ── OBSERVE ────────────────────────────────────────────────────────
	startObserve := func(ch <-chan storage.ConversationEntry) func() error {
		var wg sync.WaitGroup
		var once sync.Once
		var firstErr error

		for range cfg.ObserveGoroutines {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for entry := range ch {
					if observeCtx.Err() != nil {
						return
					}
					if err := cfg.Observe(observeCtx, entry); err != nil {
						once.Do(func() {
							firstErr = err
							observeCancel()
						})
						return
					}
				}
			}()
		}

		return func() error {
			wg.Wait()
			return firstErr
		}
	}

	if pq == nil {
		// No limit: close observe channel when uploads finish.
		go func() {
			uploadWg.Wait()
			close(observeCh)
		}()

		waitObserve := startObserve(observeCh)
		if err := waitObserve(); err != nil {
			return nil, err
		}
	} else {
		// With limit: observe goroutines start immediately. A drainer
		// goroutine watches watermarks and pushes finalized entries.
		waitObserve := startObserve(observeCh)

		// Signal when all upload goroutines have finished.
		uploadsDone := make(chan struct{})
		go func() {
			uploadWg.Wait()
			close(uploadsDone)
		}()

		var drainedCount int
		go func() {
			defer close(observeCh)

			// Wait for either: watermarks confirm the top N (early
			// termination), or all uploads finish (fallback).
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()

			earlyTerminated := false
			for {
				select {
				case <-observeCtx.Done():
					return
				case <-uploadsDone:
					// All uploads finished. Priority queue has all candidates.
				case <-ticker.C:
					if pq.TryFinalize(wm) {
						// Top N confirmed. Cancel discover/upload.
						discoverCancel()
						earlyTerminated = true
					} else {
						continue
					}
				}
				break
			}

			if earlyTerminated {
				// Wait for upload goroutines to exit after cancellation.
				<-uploadsDone
			}

			entries := pq.Drain()
			drainedCount = len(entries)
			for _, entry := range entries {
				select {
				case observeCh <- entry:
				case <-observeCtx.Done():
					return
				}
			}
		}()

		if err := waitObserve(); err != nil {
			return nil, err
		}

		result.Observed = drainedCount
		total := int(needsObserveCount.Load())
		result.Remaining = total - drainedCount
	}

	result.Skipped = int(skipCount.Load())
	if pq == nil {
		result.Observed = int(needsObserveCount.Load())
	}

	return &result, nil
}

// watermarks tracks per-source low watermarks for priority queue finalization.
type watermarks struct {
	mu      sync.Mutex
	sources map[string]*sourceWatermark
}

type sourceWatermark struct {
	oldest    time.Time // oldest timestamp produced so far
	expected  int       // total conversations the source will produce
	processed int       // conversations processed by upload goroutines
	done      bool      // true when all conversations are processed
}

func newWatermarks(sourceNames []string) *watermarks {
	sources := make(map[string]*sourceWatermark, len(sourceNames))
	for _, name := range sourceNames {
		sources[name] = &sourceWatermark{}
	}
	return &watermarks{sources: sources}
}

// Update records a conversation timestamp from the given source.
func (w *watermarks) Update(source string, ts time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sw := w.sources[source]
	if sw == nil {
		return
	}
	if sw.oldest.IsZero() || ts.Before(sw.oldest) {
		sw.oldest = ts
	}
}

// SetExpected records how many conversations a source will produce. Called
// after the provider's Conversations() returns but before conversations are
// processed by upload goroutines.
func (w *watermarks) SetExpected(source string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sw := w.sources[source]; sw != nil {
		sw.expected = n
		if n == 0 {
			sw.done = true
		}
	}
}

// RecordProcessed increments the processed count for a source. When all
// expected conversations have been processed, the source is marked done.
func (w *watermarks) RecordProcessed(source string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sw := w.sources[source]; sw != nil {
		sw.processed++
		if sw.expected > 0 && sw.processed >= sw.expected {
			sw.done = true
		}
	}
}

// MarkDone signals that a source has finished producing conversations (e.g.
// on error, where no conversations will be processed).
func (w *watermarks) MarkDone(source string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if sw := w.sources[source]; sw != nil {
		sw.done = true
	}
}

// AllDone returns true if every source has finished.
func (w *watermarks) AllDone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, sw := range w.sources {
		if !sw.done {
			return false
		}
	}
	return true
}

// CanFinalize returns true if the given timestamp (the oldest entry in a full
// priority queue) is newer than every active source's watermark. A source that
// is done or whose watermark is older than the timestamp cannot produce anything
// that would displace the Nth entry.
func (w *watermarks) CanFinalize(oldest time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, sw := range w.sources {
		if sw.done {
			continue
		}
		if sw.oldest.IsZero() {
			return false
		}
		if !oldest.After(sw.oldest) {
			return false
		}
	}
	return true
}

// priorityQueue is a bounded, mutex-synchronized min-heap of ConversationEntry
// keyed by LastModified. The oldest entry sits at the root so it can be evicted
// when a newer entry arrives and the queue is full.
type priorityQueue struct {
	mu       sync.Mutex
	entries  []storage.ConversationEntry
	capacity int
}

func newPriorityQueue(capacity int) *priorityQueue {
	return &priorityQueue{
		entries:  make([]storage.ConversationEntry, 0, capacity),
		capacity: capacity,
	}
}

// Insert adds an entry to the queue. If the queue is full and the entry is
// newer than the oldest, the oldest is evicted. If the entry is older than
// everything in a full queue, it is discarded.
func (pq *priorityQueue) Insert(entry storage.ConversationEntry) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if len(pq.entries) < pq.capacity {
		pq.entries = append(pq.entries, entry)
		pq.siftUp(len(pq.entries) - 1)
		return
	}

	if entry.LastModified.After(pq.entries[0].LastModified) {
		pq.entries[0] = entry
		pq.siftDown(0)
	}
}

// TryFinalize checks if the queue is full and the watermarks confirm the top N.
func (pq *priorityQueue) TryFinalize(wm *watermarks) bool {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	if len(pq.entries) < pq.capacity {
		return false
	}
	return wm.CanFinalize(pq.entries[0].LastModified)
}

// Drain returns all entries sorted newest-first and empties the queue.
func (pq *priorityQueue) Drain() []storage.ConversationEntry {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	out := make([]storage.ConversationEntry, len(pq.entries))
	copy(out, pq.entries)
	pq.entries = pq.entries[:0]

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].LastModified.After(out[j-1].LastModified); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Len returns the current number of entries.
func (pq *priorityQueue) Len() int {
	pq.mu.Lock()
	defer pq.mu.Unlock()
	return len(pq.entries)
}

func (pq *priorityQueue) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if !pq.entries[i].LastModified.Before(pq.entries[parent].LastModified) {
			break
		}
		pq.entries[i], pq.entries[parent] = pq.entries[parent], pq.entries[i]
		i = parent
	}
}

func (pq *priorityQueue) siftDown(i int) {
	n := len(pq.entries)
	for {
		smallest := i
		left := 2*i + 1
		right := 2*i + 2
		if left < n && pq.entries[left].LastModified.Before(pq.entries[smallest].LastModified) {
			smallest = left
		}
		if right < n && pq.entries[right].LastModified.Before(pq.entries[smallest].LastModified) {
			smallest = right
		}
		if smallest == i {
			break
		}
		pq.entries[i], pq.entries[smallest] = pq.entries[smallest], pq.entries[i]
		i = smallest
	}
}
