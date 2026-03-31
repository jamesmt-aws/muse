# Pipeline Overlap: Discover / Observe

## Problem

Today, `muse.Upload` waits for every provider to finish discovering conversations before returning.
`compose.RunClustered` then lists all conversations from the store, fingerprint-checks each one,
and dispatches pending conversations to observe goroutines. Once all discovery finishes, observation
can start. No observation starts until every source has finished discovering.

Local sources finish in the time it takes to scan a directory. API sources (GitHub, Slack) make
hundreds of HTTP requests across paginated endpoints. The observe goroutines sit idle for the
duration of the slowest source.

The Flow principle in performance.md says: "Barriers exist only where the next stage requires the
complete output of the previous stage." Observe does not require all conversations to be known
before it can start processing.

## Current flow

Each source provider runs in its own goroutine (`muse.go:160-181`). All providers must finish
(`wg.Wait()`) before the diff-and-upload loop begins (`muse.go:214-245`, `g.SetLimit(20)`). After
Upload returns, `RunClustered` calls `runObserve` (`clustered.go:60`), which lists conversations
from the store, builds a pending list, sorts it, and dispatches to 50 observe goroutines
(`clustered.go:394-395`). The map-reduce path (`compose.go:74-129`) follows the same pattern with
a semaphore of 8.

The pipeline has three sequential barriers: all providers finish, then all uploads finish, then
observe begins. Both composition methods (clustering and map-reduce) share these barriers.

## Design

The pipeline replaces those three barriers with two channels. The change applies to both
composition methods.

Each source provider pushes conversations into an **upload channel** as it finds them. A pool of
upload goroutines reads from the upload channel, diffs against the store, uploads changed
conversations, fingerprint-checks against existing observations, and pushes conversations that need
observation into an **observe channel**. Observe goroutines read from the observe channel and run
the LLM call immediately.

A source provider finishes when its `Conversations()` call returns. Each provider goroutine
runs `Conversations()`, iterates the result, sends each conversation to the upload channel with a
`select` against `ctx.Done()`, and exits. A `sync.WaitGroup` tracks active provider goroutines.
When the last provider goroutine exits, the upload channel closes. A second `WaitGroup` tracks
upload goroutines. When the last upload goroutine drains the upload channel and exits, the observe
channel closes. When all observe goroutines drain the observe channel, the pipeline proceeds to
label (clustering) or compose (map-reduce).

Each source gets one provider goroutine. Upload goroutines default to 20 (matching today's
`g.SetLimit(20)` in `muse.go:229`). Observe goroutines default to 50 for clustering (matching
today's `g.SetLimit(50)` in `clustered.go:395`) and 8 for map-reduce (matching today's semaphore
in `compose.go:140`). All goroutines are persistent for the duration of the pipeline and read from
their channel in a loop until it closes. Both channels are buffered at 32 to smooth out bursts
between stages without hiding backpressure behind large buffers. Every channel send uses `select`
against `ctx.Done()` so that context cancellation (from an observe error or interrupt) unblocks any
goroutine blocked on a full channel.

### Fingerprint check

A fingerprint is a hash of the conversation's last-modified timestamp and the current prompt chain
(observe + refine prompts). If a conversation's fingerprint matches its stored observation, the
conversation has not changed since it was last observed and does not need reprocessing. Upload
goroutines compute the fingerprint after reading from the upload channel. Conversations that match
their stored fingerprint are skipped. Conversations that don't match are pushed to the observe
channel.

With `--reobserve`, all stored observations are deleted before the channels open. Every fingerprint
check misses, so every conversation flows through to observe. When `--reobserve` is combined with
`--limit N`, the priority queue (described below) selects the N newest from the full set.

### Reproducing current behavior

The current sequential pipeline is a degenerate case of the streaming model: set both channel
buffers to 0 and add a `sync.WaitGroup` barrier between the last upload goroutine exiting and
the first observe goroutine starting. The design does not offer this as a mode, but the
equivalence confirms that streaming is a strict generalization.

## Limit enforcement

`--limit N` caps how many conversations are observed per run. Today the limit is applied after
sorting all pending conversations newest-first (`clustered.go:350-357`). With streaming,
conversations arrive in discovery order, so enforcing a newest-first limit requires coordination
across sources.

Two simpler alternatives exist. First-conversation-wins treats the limit as a counter on the
observe channel and observes whatever arrives first. First-wins is unfair to remote sources: local
sources complete faster and consume most of the budget before any API conversation arrives. Even
distribution divides N equally among sources, which avoids starvation but introduces recency bias:
a source with two new conversations and a source with two hundred both get N/S slots. Neither
alternative guarantees that the N most recent conversations are selected.

The design uses a priority queue to guarantee newest-first selection across all sources.

Upload goroutines that find a conversation needing observation do not push it directly to the
observe channel. Instead, they insert it into a shared bounded priority queue sorted newest-first,
with capacity N. The queue is accessed by multiple upload goroutines concurrently and requires
mutex synchronization. When the queue is full and a new conversation is newer than the oldest
entry, the oldest entry is evicted. When the new conversation is older than every entry in the
queue, it is discarded.

Each source also reports a **low watermark**: the timestamp of the oldest conversation it has
produced so far. When a source finishes, its watermark drops to zero (it will produce nothing
more).

Once the priority queue holds N conversations and the Nth conversation's timestamp is newer than
every source's low watermark, the top N are finalized. The pipeline cancels the context for
remaining source providers (they may still be fetching from a slow API) and drains the finalized
conversations into the observe channel.

### Timestamp assumptions

The priority queue depends on timestamps. For local sources, the timestamp is file modification
time. For GitHub and Slack, the timestamp is the API's `updated_at` field.

If timestamps are wrong (clock skew, stale API metadata, filesystem that doesn't preserve mtime),
the priority queue selects the wrong N conversations. The failure is soft: the pipeline observes
slightly older conversations instead of the truly newest, and the next run picks up what was
missed. No data is lost.

If a source produces a conversation with a timestamp far in the future (corrupted mtime, clock
skew), that conversation occupies a priority queue slot it shouldn't and may evict a legitimately
recent conversation. The evicted conversation is picked up on the next run.

If a source's timestamps are entirely unordered, its watermark provides no information until the
source finishes. The pipeline still works correctly, but that source cannot benefit from early
termination. All other sources can still terminate early once their watermarks and the queue
confirm the top N.

### Source ordering contract

The watermark mechanism requires sources to produce conversations in roughly newest-first order.
Local sources can sort by modification time before producing. API sources with incremental sync
(GitHub, Slack) already fetch newest-first. A source that cannot guarantee ordering must finish
completely before its watermark carries information. Add the ordering contract to
`designs/sources.md` as part of the Provider interface specification.

If `--limit` is not set, the priority queue is bypassed. Upload goroutines push directly to the
observe channel and sources run to completion. There is no default limit. A default would silently
skip conversations, which is surprising behavior for a tool that should process everything it
finds.

## Progress display

When `--limit N` is set, the observe progress bar has a known ceiling: min(N, pending). The bar
shows definite progress from the start. The discover progress bar still grows as sources report
conversations, but the observe bar is bounded.

When no limit is set, the total conversation count is unknown until all sources finish discovering.
The discover progress bar starts with an unknown total and updates as sources complete. The observe
progress bar may start before the discover total is known, showing a count without a denominator
until discovery finishes. Once all sources complete and all fingerprint checks run, the observe
total is known and the bar switches to definite progress.

## Dispatch order

Today, pending conversations are re-sorted largest-first so the most expensive work starts
immediately (`clustered.go:358-362`, per the Order principle in performance.md). With streaming,
conversations arrive as discovered. Start without largest-first reordering. If observe wall-clock
time increases measurably on real workloads, the upload goroutines can buffer a small window and
sort before pushing to the observe channel.

## Error handling

Today, `Conversations()` returns all conversations from a source or an error. A single malformed
conversation file fails the entire source. With streaming, errors should be more granular.

A conversation-level error (bad JSON, missing required field) is logged and skipped. The provider
goroutine continues producing the remaining conversations from that source. A partially readable
conversation (valid JSON with some unparseable fields) should still be produced if enough structure
survives to be useful. The observe LLM is tolerant of incomplete input.

A source-level error (directory does not exist, API authentication failure) closes that provider's
goroutine but does not affect the upload channel. Other provider goroutines continue.

An observe error cancels the pipeline context, which unblocks all goroutines blocked on channel
sends via `select` on `ctx.Done()`.

## Telemetry

Today, `runObserve` returns an `observeResult` with usage, processed count, pruned count,
remaining count, and data size. Stage timing is measured with `time.Since(observeStart)` and
reported via `logBefore`/`logAfter` (`clustered.go:87-89`).

With streaming, observe starts before discover finishes. The observe start time is no longer a
clean boundary. Stage timing should measure from the first conversation entering the observe
channel to the last observation completing. Token usage and processed/pruned counts accumulate
across goroutines via atomic counters (same pattern as today). The discover and observe log lines
overlap in time, which the progress display already needs to handle (see Progress display above).

Update `004-clustered-compose.md` pipeline documentation to reflect that discover, upload, and
observe run concurrently rather than sequentially.

## Files

Best-guess list of files to create or modify.

**New:**
```
internal/pipeline/streaming.go       channel orchestration, WaitGroups, priority queue
internal/pipeline/streaming_test.go  unit tests for channel lifecycle and limit enforcement
```

**Modified:**
```
internal/muse/muse.go                Upload() restructured to push to upload channel
internal/compose/clustered.go        runObserve() reads from observe channel instead of dispatching
internal/compose/compose.go          Run() observe loop reads from observe channel
internal/conversation/types.go       Provider interface: document ordering contract
designs/sources.md                   add source ordering contract to Provider specification
designs/004-clustered-compose.md     update pipeline docs for concurrent discover/upload/observe
performance.md                       update Flow violation (Upload/Observe barrier resolved)
```

## What stays the same

Label, theme, group, summarize, and compose all require the complete set of observations before
they start. Today the pipeline blocks before observe begins: all conversations must be discovered,
uploaded, and listed before any observation starts (`clustered.go:60-74`). After this change, the
pipeline blocks after observe finishes and before label begins. Observe runs concurrently with
discover and upload, but label still waits for every observation to complete.
