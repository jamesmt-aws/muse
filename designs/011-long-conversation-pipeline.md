# Long-Conversation Pipeline

## Problem

The default observation pipeline feeds the full compressed conversation to the observe prompt
in one pass. On long conversations this fails starkly: the prompt receives more material than
it can attend to, and reasoning from early turns gets washed out by mechanical content
(tool output, formatting, file diffs) from later turns. Thompson and Tarn (2026) document the
failure on a 27-turn RFC design conversation containing six messages with clear reasoning,
where the default pipeline returned zero observations. Five further variants of the default
approach also failed: scaling assistant text by message length, changing the observe prompt,
asking the LLM to reason about context needs, filtering confirmations by pattern, and LLM
triage before observation.

Better observation alone is not enough. When extraction does succeed at higher volume, the
composition pipeline (sample → summarize) compresses the additional observations into
generic summaries, losing the specifics that windowed extraction recovered. Each stage's
fix exposes the next stage's bottleneck.

## Solution

A chain of four fixes that operate on the same conversations through the existing six-stage
pipeline (Observe, Label, Cluster, Sample, Summarize, Compose). The chain is a unit: shipping
any fix without the others either leaves the symptom (windowed observation without composition
fixes produces more raw observations that get diluted) or reverses the goal (composition
fixes without windowed observation have nothing to operate on).

### Windowed owner-only observation

`extractWoo` slides an 8-turn window across the conversation with a stride of 4 turns, strips
assistant text from each window, and observes each window independently. Windows of
mechanical content return NONE at negligible cost; reasoning windows produce observations.
Observations from overlapping windows are deduplicated by text containment, then refined.

The window size of 8 and stride of 4 follow Thompson and Tarn (2026) §2.2. The 4-turn
overlap halves the window, ensuring no reasoning sequence is split across windows in a way
that loses context.

Stripping assistant text serves two ends documented in §2.4. The assistant's output competes
for the observe prompt's attention while diluting the owner's reasoning signal. And at
matched context sizes, assistant text raises the misleading observation rate from 7% to 16%
(§2.4 Table 3): the observer hallucinates reasoning patterns more often when it can see both
sides of the conversation than when it sees only the owner.

Per-window observe calls use a `MaxTokens` budget of `windowObserveBudget` (4096) and
retry once at `windowObserveRetryBudget` (16384) on truncation. Truncation usually means a
thinking-by-default model spent the whole budget on `<think>` tokens before emitting any
content; the retry gives it room to finish. A window that still truncates at the retry
budget is skipped (no observation for that window) rather than failing the whole
conversation. Adjacent overlapping windows usually cover the same material, so a single
skipped window rarely costs information.

Empirical baseline from §2.5, Table 4 (full corpus, 453 conversations): the default
pipeline produces 69 observations at 84% grounding rate; windowed owner-only produces 273
observations at 91% grounding rate. The grounding rate increase is what makes the volume
delta useful rather than noise.

### Adaptive fallback

`extractAdaptive` tries `extractWoo` first on each window. If `extractWoo` returns NONE for a
window, it falls back to the default observation method (with assistant text included) on
the same window. At most two LLM calls per window. Catches the windows where terse owner
messages only make sense with assistant context.

§2.5 Table 4: adaptive produces 562 observations at 88% grounding rate. The grounding rate
drops slightly vs woo-only (91% → 88%) because the fallback path includes assistant text,
which raises the misleading rate. The trade is more raw grounded observations in absolute
terms (494 vs 249) at a small grounding-rate cost.

Both strategies are exposed via a `--extract` flag on `muse compose`:

```
muse compose                       # default: full-conversation observe (mainline behavior)
muse compose --extract woo         # windowed owner-only
muse compose --extract adaptive    # woo first, default fallback per window
```

### Quote-prioritized sampling

The cluster sample stage selects observations up to a token budget per cluster. Default
behavior (§3.2) shuffles randomly, so the budget often fills before quoted observations get
a chance.

The fix partitions observations into two groups: those with verbatim owner quotes and those
without. Each group is shuffled, and quoted observations fill the budget first. No LLM calls.

This matters proportionally to cluster size. With ~5 observations per cluster (mainline
volume, §3.5 Table 5), the partition rarely changes selection; the budget is large enough
to fit everything. With ~25 observations per cluster (adaptive volume), the partition is
load-bearing: random sampling regularly drops every quoted observation in a cluster.

### Exemplar prompt

The summarize prompt gets two appended sentences:

> Summarize the pattern first, then include one or two verbatim quotes that illustrate the
> pattern in action. The summary tells the reader what the person does. The quotes show them
> doing it.

This is the load-bearing finding for the bundle. Thompson and Tarn (2026) §3.3 show that
without the structural guidance, the summarize prompt collapses larger observation sets into
generic summaries ("tracks linguistic precision across multiple dimensions") that lose the
specifics ("I would never say ablate", "colons paper over conceptual gaps", "negative
definitions are a pathology"). With the guidance, the summary leads with the pattern and
illustrates it with quoted material the upstream sampling fix preserved.

The sentence composes with mainline's three-signal taxonomy in `prompts/summarize.md` (the
generative-principles / decision-heuristics / terminal-rules framing). The taxonomy tells
the model *what* to capture; the new sentence tells it *how to structure* the output.

### Cache fingerprinting

Observation fingerprints today (`004-clustered-compose.md`, "Caching") hash the conversation
timestamp and the prompt chain. With `--extract` set, the fingerprint folds in
`prompts.ObserveWindowed` and the strategy name (`"woo"` or `"adaptive"`):

```
fingerprint = hash(
    conversation.LastModified,
    observePromptHash,
    observeHumanPromptHash,
    refinePromptHash,
    [observeWindowedPromptHash, strategy] when --extract is set
)
```

Switching strategies invalidates the cache cleanly. Default (`--extract` unset) produces the
same fingerprint mainline would; existing caches stay valid.

### When not to use it

Short conversations (under one window's worth of turns, ~8 turns) get no benefit from
windowing and pay the per-window LLM overhead. The default path remains correct. The
`--extract` flag is opt-in; the default is mainline behavior.

## Decisions

### Why the four fixes ship together rather than independently

§6 of Thompson and Tarn (2026) makes the case explicitly: "Each fix reveals the next
constraint. The observation problem was invisible until conversations got long enough to
trigger it. The composition problem was invisible until observations got numerous enough to
trigger it." Quote-prioritized sampling and the exemplar prompt have negligible measurable
effect at mainline observation volumes (~5 obs/cluster); their value appears only when
windowed extraction grows clusters to ~10-25 observations.

The implementation PR keeps the bundle atomic for the same reason a revert would need to be
atomic: half the bundle is worse than mainline (more raw observations, generic summaries) or
silently equivalent (composition fixes with no extra observations to compose).

### Why a flag rather than automatic-by-conversation-length

A length threshold is a tempting alternative: "auto-windowed for conversations over N
tokens." Rejected for three reasons:

1. The threshold is model-dependent. A model with 200k context handles longer conversations
   in one pass than a model with 32k. Hardcoding N misses both directions.
2. Cache fingerprints would need to encode the threshold and the conversation's place
   relative to it, which leaks pipeline policy into stored observations.
3. Reproducibility during research requires the strategy be a deterministic input, not
   inferred. Manually specified flag keeps experiments diff-able.

If a future use case argues for auto-by-length on top of the flag (e.g., a `--extract auto`
mode that picks woo above N tokens), the flag-based design accommodates it as a third
strategy value without restructuring.

### Why `prompts.ObserveWindowed` instead of folding into `prompts.Observe`

The windowed observation prompt is tuned for owner-only input (8 turns of human messages,
no assistant context). The default `prompts.Observe` is tuned for full compressed
conversations with assistant text. The two prompts ask the same question but optimize for
different input shapes. Folding them risks regressing one of the two cases.

A future iteration may consolidate if both prompts converge to the same text after enough
tuning. Keeping them separate today preserves the option.

### Why quote-prioritized sampling sits in `runSampleWithObs` rather than upstream

Quote presence is a property of the observation, not of the conversation. The earliest place
to apply quote-prioritization is the sample stage, which already iterates per-cluster with
a token budget. Earlier stages (label, cluster) don't know about budgets or quotes; later
stages (summarize) consume the sampled set as-is. The sample stage is the natural home.

## Deferred

### Quality gate (§3.4)

Thompson and Tarn (2026) §3.4 documents a fifth fix: an Opus-tier judge rates each
observation against its source conversation as grounded, generic, or misleading; only
grounded observations flow into composition. Reduces misleading rate from 7-12% to under 2%.
Costs ~$0.02/conversation, cacheable across pipeline runs.

Not in this design. Lands as `012-observation-quality-gate.md` and its own implementation PR
after this bundle is in. The quality gate is conceptually independent: it improves any
observation pipeline (mainline, windowed, adaptive) by filtering observations before
composition. It belongs in a separate design because its argument is "filter for quality,"
not "fix long-conversation extraction."

### Skill discovery (§4)

Thompson and Tarn (2026) §4 explored an EM-inspired iterative skill-discovery approach
against a one-shot baseline. The iterative approach lost on every metric (lower max
affinity, higher orphan rate, never converged). Documented in the paper as a null result.
No implementation work warranted.

### Auto-by-length strategy selection

The flag could grow an `auto` mode that picks a strategy based on conversation token count.
See "Why a flag rather than automatic-by-conversation-length" above for why this isn't the
default. Add when there's a use case that argues for it (e.g., a corpus where most
conversations are short and the user wants windowing only for outliers).

### Streaming windowed extraction

Windowed extraction processes windows sequentially today, deduplicating after all windows
return. A streaming variant could emit observations as windows complete, useful for
interactive feedback during long conversations. Out of scope; the batch path is sufficient
for compose's use case.
