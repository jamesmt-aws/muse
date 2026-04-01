# Streaming Discover / Observe

## Problem

Composing a muse runs five sequential stages: discover, upload, observe, label, compose. The first
three share a property the last two don't — each conversation can be processed independently.
Despite this, the pipeline forces a full barrier between stages: all conversations must be
discovered before any are uploaded, and all must be uploaded before any are observed.

Local sources finish in the time it takes to scan a directory. API sources (GitHub, Slack) make
hundreds of HTTP requests across paginated endpoints. The observe goroutines sit idle for the
duration of the slowest source.

The Flow principle in performance.md says: "Barriers exist only where the next stage requires the
complete output of the previous stage." Observe does not require all conversations to be known
before it can start processing.

## Solution

### Pipeline

Two channels replace the three barriers between discover, upload, and observe.

```
sources ─► UPLOAD CHANNEL ─► upload goroutines ─► OBSERVE CHANNEL ─► observe goroutines ─► label
```

Each source provider pushes conversations into the **upload channel** as it finds them. Upload
goroutines read from the upload channel, diff against the store, upload changed conversations,
fingerprint-check against existing observations, and push conversations that need observation into
the **observe channel**. Observe goroutines read from the observe channel and run the LLM call
immediately.

A source provider finishes when its `Conversations()` call returns. A `WaitGroup` on provider
goroutines closes the upload channel when all sources finish. A second `WaitGroup` on upload
goroutines closes the observe channel when the upload channel is drained. Label begins after the
observe channel is drained.

Both channels are buffered (default 32) to smooth out bursts. The pipeline uses two context layers:
a **discover context** for discover and upload goroutines, and an **observe context** for observe
goroutines. An observe error cancels both. Watermark-based early termination (see Limit
enforcement) cancels the discover context without affecting observe, so observe goroutines keep
processing the finalized top N while remaining sources are cancelled.

### Fingerprint check

A fingerprint is a hash of the conversation's last-modified timestamp and the current prompt chain
(observe + refine prompts). Upload goroutines compute the fingerprint after diffing against the
store. Conversations whose fingerprint matches their stored observation are skipped. Conversations
that don't match are pushed to the observe channel.

With `--reobserve`, all stored observations are deleted before the channels open. Every fingerprint
check misses, and every conversation flows through to observe.

### Limit enforcement

`--limit N` caps how many conversations are observed. Today the limit is applied after sorting all
pending conversations newest-first. With streaming, conversations arrive in discovery order, so
enforcing a newest-first limit requires coordination across sources.

Two simpler alternatives exist. First-conversation-wins observes whatever arrives first — unfair to
remote sources, since local sources complete faster and consume most of the budget. Even
distribution divides N equally among sources, which avoids starvation but introduces recency bias.
Neither guarantees the N most recent conversations are selected.

The design uses a bounded priority queue (min-heap by timestamp, capacity N) to guarantee
newest-first selection. Upload goroutines insert into the queue instead of pushing directly to the
observe channel. When the queue is full and a newer conversation arrives, the oldest is evicted.

Per-source watermarks track which sources have finished processing. A source is marked done not
when its provider goroutine returns, but when upload goroutines have processed all of its
conversations. This prevents premature finalization — a source that has pushed conversations to the
upload channel but whose conversations haven't been uploaded yet cannot be considered done.

Once the priority queue is full and every source's watermark confirms that no unprocessed
conversation could displace the Nth entry, the top N are finalized. The discover context is
cancelled (stopping remaining slow API sources), and the finalized entries drain into the observe
channel. If watermarks never confirm early, the pipeline falls back to waiting for all uploads to
finish before draining.

Without `--limit`, the priority queue and watermarks are bypassed. Upload goroutines push directly
to the observe channel. There is no default limit — a default would silently skip conversations.

### Timestamps

The priority queue depends on timestamps. For local sources, the timestamp is file modification
time. For GitHub and Slack, the timestamp is the API's `updated_at` field.

Wrong timestamps (clock skew, stale API metadata, filesystem that doesn't preserve mtime) cause
the priority queue to select the wrong N conversations. The failure is soft: the next run picks up
what was missed. A timestamp far in the future evicts a legitimately recent conversation, but that
conversation is also picked up on the next run. No data is lost.

The priority queue benefits from sources that produce conversations in roughly newest-first order.
Local sources can sort by modification time. API sources with incremental sync already fetch
newest-first. A source that cannot guarantee ordering must finish completely before its contribution
to the priority queue is meaningful.

### Error handling

A conversation-level error (bad JSON, missing required field) is logged and skipped. The provider
goroutine continues with remaining conversations. A partially readable conversation should still be
produced if enough structure survives to be useful.

A source-level error (directory does not exist, API authentication failure) closes that provider's
goroutine. Other providers continue. An observe error cancels the observe context, which also
cancels the discover context (its parent), unwinding the entire pipeline.

## Decisions

### Why not stream within a single source?

`Conversations()` returns `[]Conversation` — a batch interface. The streaming pipeline overlaps
*across* sources, not *within* a single source. Converting `Conversations()` to a channel-based
interface would allow intra-source streaming (observe page 1 of GitHub results while page 2 is
being fetched), but that requires changing every provider implementation. The cross-source overlap
captures most of the benefit because the bottleneck is the slowest source, not the internal pacing
of any one source.

### Why a min-heap instead of sorting after collection?

The priority queue bounds memory to N entries regardless of how many conversations need observation.
Sorting after collection requires holding all candidates in memory. For most workloads this
distinction doesn't matter, but the heap is simpler to reason about under concurrent insertion.

### Why buffer both channels at 32?

Unbuffered channels serialize producer and consumer — no overlap within a stage. Buffers that are
too large hide backpressure behind memory. 32 is large enough to absorb per-conversation timing
variance without masking a slow consumer.

### Why track "done" at the upload level, not the provider level?

A provider goroutine pushes conversations into the upload channel and returns. But those
conversations sit in the channel buffer until upload goroutines process them. If a source is marked
done when the provider returns, the priority queue might finalize before the source's conversations
are inserted — selecting the wrong N. Done is tracked per conversation processed by upload
goroutines, not per provider return.

## Deferred

### Map-reduce streaming

The map-reduce compose path uses the same sequential barriers. The streaming pipeline applies to
both methods in principle, but the implementation currently only wires clustering. **Revisit when:**
map-reduce is used on workloads where API source latency matters.

### Dispatch order

The legacy path sorts pending conversations largest-first so expensive work starts immediately.
The streaming path processes conversations in discovery order. **Revisit when:** observe wall-clock
time increases measurably on real workloads. If it does, upload goroutines can buffer a small
window and sort before pushing to the observe channel.

### Source ordering contract

Sources that produce conversations newest-first enable better limit enforcement. This ordering
expectation should be documented in `designs/sources.md` as part of the Provider interface
specification. **Revisit when:** a source is added that cannot produce in time order.
