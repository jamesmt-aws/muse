# Observe Strategy Comparison

## Experiment: 2026-04-07

Ran full compose (observe through muse.md) on 417-427 conversations using three
observation strategies. Same prompts, same downstream pipeline (label, theme, cluster,
summarize, thesis, compose). Only the observe stage differs.

The `--observe-mode` flag on `muse compose` selects the strategy. The flag threads
through BaseOptions to observeAndRefine, which dispatches to one of three paths for
conversations with more than 10 turns. Short conversations use the same compress +
observe + refine path regardless of mode.

### Results

| Strategy | Observations | Conversations w/ obs | Observe cost | Total cost | Time |
|---|---|---|---|---|---|
| Windowed (size 8, stride 4) | 462 | 69 | $12.15 | $13.46 | 862s |
| Triage + owner-only | 243 | 42 | $5.62 | $6.52 | 364s |
| Full conversation (baseline) | 151 | 42 | $5.65 | $6.40 | 329s |

All three produce ~100-line muse.md files. The difference is which 100 lines.

### What each strategy finds

**Windowed** finds the most craft-level specifics: named editing passes with exit
conditions, specific punctuation rules, voice authenticity tests ("I would never type
those"), artifact staleness as a failure mode, document structure as epistemic signaling,
ML jargon rejection. These are small-scale reasoning moments that live in specific turns.

**Triage + owner-only** finds decision-making patterns: where decisions should land
(operator vs app-dev), named reusable primitives (Ralph Wiggum loop, Orwell pass),
agent loop architecture, scope cutting as active discipline. Stripping assistant text
concentrates the owner's strategic reasoning.

**Full conversation (baseline)** finds analytical/philosophical depth: separating the
lens from the specimen, research methodology (substrate vs method failure),
metacognitive self-correction, weaponized vagueness. Surprisingly not empty -- the
0-observation problem only affects long conversations. Short conversations produce
observations fine under all strategies, and there are enough short conversations to
build a reasonable (if thinner) muse.

### The strategies are not converging

The three muses are not three approximations of the same portrait. They emphasize
genuinely different facets:

- Windowed catches editing craft and concrete specifics
- Triage catches decision architecture and pattern naming
- Baseline catches philosophical depth and metacognition

This means the compose step is bottlenecked on observation diversity, not volume.
More observations of the same type won't improve the muse. Different observations will.

### Diagnosis

Each strategy presents conversations differently to the observe prompt, which changes
what the LLM attends to:

- **Windowed** shows 8 turns with full assistant context. The LLM sees a specific
  interaction in detail. It catches micro-level reasoning: word choice corrections,
  naming decisions, editing moves.

- **Triage + owner-only** shows 10-15 owner messages with no assistant context. The
  LLM sees concentrated strategic reasoning. It catches macro-level patterns: where
  decisions land, how patterns get named, what gets deferred.

- **Baseline** shows the full compressed conversation. For short conversations this
  works fine. For long ones it returns NONE, but the short-conversation observations
  lean toward analytical depth because short conversations tend to be focused
  discussions rather than execution sessions.

The strategies aren't better or worse -- they're sensitive to different signal types.

## Next: multi-altitude observation

The three strategies form a granularity spectrum: windowed (8 turns with assistant
context) is the most granular, triage (owner-only from reasoning turns) is mid-altitude,
baseline (full conversation) is the widest view. Each altitude catches signal invisible
to the others.

### Could we cascade instead of union?

The naive union runs all three independently and merges observations. Cost is additive.
A cascade would run one strategy, then use its output to inform the next.

The natural cascade: window the conversation, triage the windows, construct a
higher-altitude pass from the results. But two findings constrain this:

1. **Triage within windows adds little.** Each window is 8 turns. Mechanical windows
   already return NONE naturally. Triaging within a window to get 3-4 turns is
   essentially just a smaller window -- we could achieve the same effect by reducing
   window size. The triage step is redundant at window scale.

2. **Triage on triaged turns with assistant text still fails.** From 009 approach E:
   triage correctly identifies reasoning turns, but observe on triaged turns with
   compressed assistant text returns NONE. This was tested on full conversations, not
   windows. On windows the problem might not apply (they're small enough). But we
   haven't tested this, and the mechanism is still unexplained.

What triage adds to windowed isn't finer filtering -- it's a different altitude.
Windowed observes 8-turn interactions. Triage observes 10-15 owner messages spanning
the whole conversation. The second pass sees cross-conversation arcs that no single
window contains: how the owner's position evolved, what patterns recur across decisions,
what got deferred and why.

### The right framing: multi-altitude, not multi-strategy

If both altitudes find unique signal, the pipeline should run both as a single
pipeline, not as two separate strategies the user chooses between. The user shouldn't
need to know about observation strategies. They run `muse compose` and get the best
muse we can build.

The pipeline would be:
1. **Window pass:** sliding windows with assistant context, observe each, deduplicate
2. **Altitude pass:** triage + owner-only on the full conversation
3. **Merge:** combine observations from both passes, deduplicate
4. **Downstream:** label, theme, cluster, summarize, thesis, compose as today

Cost is ~$18 observe for the current corpus. That's 2.7x baseline. Whether this is
acceptable depends on how often users run compose (daily? weekly?) and whether the
quality improvement justifies it. The downstream stages ($1-2) are unchanged.

### Experiment to run

1. Run windowed and triage on the same corpus (reuse cached observations or run fresh)
2. Merge observation artifacts, deduplicating by text similarity
3. Compose from the merged set
4. Compare the resulting muse against windowed-only and triage-only
5. Judge using the same LLM-as-judge dimensions we use for generative evals

This is a single-variable test: same conversations, same downstream pipeline, only the
observation set differs. If the merged muse is clearly better, build multi-altitude
into the default pipeline. If it's roughly equivalent to windowed alone, the triage
pass is waste and windowed is the answer.

### Cost note

Windowed is 2x the observe cost of triage ($12 vs $6) because it makes 5-68 observe
calls per conversation (one per window) plus a refine call. The windows run sequentially
within a conversation but conversations run in parallel. Observe dominates total cost
(90% for windowed, 86% for triage).

If windowed becomes the default, the main cost lever is window size. Larger windows
mean fewer calls but risk reintroducing context rot. The current 8/4 split is untested
against alternatives.
