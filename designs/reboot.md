# Context Preservation: Reboot

## Where we are

Branch `preserve-context` has a working implementation: mechanical filter → LLM triage →
owner-only observe. It turns 0-observation conversations into 7-9 observation conversations.
Not yet pushed or PR'd. The design doc (009-observations.md) documents five abandoned
approaches and the working one.

## What we learned

The observation pipeline has a context rot problem. Conversations start with design
decisions and end with execution. The observe prompt processes the full conversation
and loses the early reasoning signal under later mechanical noise.

Five approaches failed by treating this as a compression, prompt, or filtering problem:

- **Adaptive compression** (scale assistant text by owner message length): more context
  diluted signal. 17k chars produced 0 observations, 13k produced 5.
- **Prompt-driven extraction** (ask for transformations not principles): made the LLM
  more cautious, produced fewer observations.
- **Context-aware prompting** (ask the LLM to reason about what context is needed):
  same regression.
- **Mechanical turn filtering** (remove confirmations by pattern matching): insufficient,
  many low-signal turns survive patterns.
- **LLM triage + compressed conversation**: triage correctly identifies reasoning turns,
  but observe still returns NONE on the compressed triaged conversation.

The breakthrough: stripping assistant text entirely and feeding observe only the owner's
messages from triaged turns. 6 curated owner messages → 5 observations. 27 mixed messages
→ 0. The problem is volume and attention, not missing context.

## The real problem: local signal, global observation

Context rot is a local signal problem. Turn 4 is a design decision regardless of what
turns 15-27 contain. But the current pipeline evaluates the full conversation at once.
The LLM reads sequentially, and mechanical turns wash out earlier reasoning turns.

The triage + owner-only approach is a workaround. It strips noise so observe sees
concentrated signal. But it's still observing the full conversation's worth of owner
messages at once, and it requires an extra LLM call for triage.

## Next step: windowed observation

Observe in sliding windows of 5-10 turns. Each window is small enough that local signal
survives. No turn is evaluated in the context of distant mechanical turns.

The pipeline would be:
1. Extract turns as today
2. Walk through turns in overlapping windows (e.g. 8 turns, stride 4)
3. Run observe on each window independently
4. Collect all observations, deduplicate

This treats context rot at the source (temporal locality) rather than the symptom
(filtering). Each window captures reasoning in its local context. Mechanical windows
produce NONE, reasoning windows produce observations, and that's correct.

Questions to resolve:
- Window size and stride. Too small loses multi-turn reasoning arcs. Too large
  reintroduces context rot.
- Deduplication. Adjacent windows overlap and may extract the same observation twice.
- Cost. More observe calls per conversation. Offset by smaller inputs per call.
- Whether windows need assistant context or owner-only. The current finding says
  owner-only works, but that was tested on globally-triaged turns, not local windows.

## Files changed on preserve-context branch

- `cmd/compose.go` — ContextStrategy flag (can be removed if we drop adaptive)
- `cmd/eval_context.go` — test harness for comparing strategies on single conversations
- `cmd/root.go` — registered eval-context command
- `designs/009-observations.md` — compression section with findings
- `internal/compose/clustered.go` — observeAndRefine with triage + owner-only path
- `internal/compose/compose.go` — ContextStrategy type, filterLowSignalTurns, triageTurns
- `prompts/observe.md` — agent-directed work paragraph
- `prompts/prompts.go` — triage embed
- `prompts/triage.md` — triage prompt

## Test harness

`muse eval-context <conversation-id-or-path>` runs the observe pipeline under each
strategy on a single conversation and prints observations side by side. Useful for
iterating on the pipeline without running full compose. Snapshot conversations to /tmp
for repeatable comparisons (conversations grow between runs).
