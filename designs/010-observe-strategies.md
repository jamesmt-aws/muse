# Observe Strategy Comparison

## Corpus experiment: 2026-04-07

Ran full compose (observe through muse.md) on 417-430 conversations using four
observation strategies. Same prompts, same downstream pipeline (label, theme, cluster,
summarize, thesis, compose). Only the observe stage differs.

The `--observe-mode` flag on `muse compose` selects the strategy. The flag threads
through BaseOptions to observeAndRefine, which dispatches for conversations with more
than 10 turns. Short conversations use the same compress + observe + refine path
regardless of mode.

### Results

| Strategy | Observations | Conversations w/ obs | Clusters | Total cost |
|---|---|---|---|---|
| Multi-zoom (default) | 658 | 82 | 25 | $18.25 |
| Windowed (size 8, stride 4) | 462 | 69 | 22 | $13.46 |
| Triage + owner-only | 243 | 42 | 21 | $6.52 |
| Full conversation (baseline) | 151 | 42 | 19 | $6.40 |

All four produce ~100-line muse.md files. The difference is which 100 lines.

### What each zoom level finds

**Windowed** (local zoom) finds craft-level specifics: named editing passes with exit
conditions, specific punctuation rules, voice authenticity tests ("I would never type
those"), artifact staleness as a failure mode, document structure as epistemic signaling,
ML jargon rejection. These are small-scale reasoning moments that live in specific turns.

**Triage + owner-only** (global zoom) finds decision-making patterns: where decisions
should land (operator vs app-dev), named reusable primitives (Ralph Wiggum loop, Orwell
pass), agent loop architecture, scope cutting as active discipline. Stripping assistant
text concentrates the owner's strategic reasoning.

**Full conversation (baseline)** finds analytical/philosophical depth: separating the
lens from the specimen, research methodology (substrate vs method failure),
metacognitive self-correction, weaponized vagueness. Surprisingly not empty -- the
0-observation problem only affects long conversations. Short conversations produce
observations fine under all strategies.

**Multi-zoom** (windowed + triage, merged) combines both zoom levels. It produced
sections that neither strategy found alone: "Who decides, not what's possible" (decision
allocation, knowledge topology) and "The distribution, not the number" (metrics as
hypotheses, signal stratification). It preserved most of the craft-level detail from
windowed while adding the strategic reasoning from triage.

### The strategies are not converging

The three single-strategy muses are not three approximations of the same portrait.
They emphasize genuinely different facets:

- Windowed catches editing craft and concrete specifics
- Triage catches decision architecture and pattern naming
- Baseline catches philosophical depth and metacognition

The compose step is bottlenecked on observation diversity, not volume. More observations
of the same type won't improve the muse. Different observations will.

### Why different zoom levels find different signal

Each strategy presents conversations differently to the observe prompt, which changes
what the LLM attends to:

- **Windowed** shows 8 turns with full assistant context. The LLM sees a specific
  interaction in detail. It catches micro-level reasoning: word choice corrections,
  naming decisions, editing moves.

- **Triage + owner-only** shows 10-15 owner messages with no assistant context. The
  LLM sees concentrated strategic reasoning spanning the full conversation. It catches
  macro-level patterns: how a position evolved, what patterns recur, what got deferred.

- **Baseline** shows the full compressed conversation. Works for short conversations.
  For long ones it returns NONE, but the short-conversation observations lean toward
  analytical depth because short conversations tend to be focused discussions.

## Orwell experiment

Separate experiment applying the three single strategies to Orwell's "Politics and the
English Language" (~5,000 words). Results in orwellian-observation.md. The essay has
known structure, making hand-verification possible.

| Strategy | Observations | Strongest section |
|---|---|---|
| Baseline (full essay) | ~54 + 5 cross-cutting | Even spread, generic on catalog/prescription |
| Windowed (5 windows) | ~34 raw, 23 deduped | Catalog (structural insights from close reading) |
| Triage (reasoning only) | ~42 + 4 cross-cutting | Diagnosis (19 obs vs baseline's 13) |

Even on a 5,000-word essay -- shorter than where context rot typically appears -- the
strategies found different signal:

- **Windowed found structural patterns in the catalog** that baseline treated as a list:
  metaphor lifecycle model, escalating severity across categories, substitution as proof
  technique. Close reading of a contained section yields insights that sequential reading
  of the full text glosses over.

- **Triage concentrated on the diagnosis section** and found the sharpest rhetorical
  strategy observations: self-deception as a service ("partially concealing your meaning
  even from yourself"), "designed" deployed as the strongest verb only after evidence
  accumulates, clarity framed as loss of capability rather than gain of wisdom.

- **All three converged** on: bidirectional causation, steelmanning before dismantling,
  epistemic calibration, self-incrimination, concrete vs abstract as master distinction,
  the pacification juxtaposition, Rule 6 as override.

The Orwell experiment confirms the corpus finding: zoom level determines what signal
survives, even when context rot isn't the problem.

## Decision: multi-zoom as default

Multi-zoom is the default observe mode. It runs both windowed (local) and triage +
owner-only (global) passes on each large conversation, merges the observations, and
stores the combined set. Small conversations (10 turns or fewer) get a single pass.

The pipeline for large conversations:
1. **Local pass:** sliding windows (size 8, stride 4) with assistant context, observe
   each, deduplicate across overlapping windows, refine
2. **Global pass:** mechanical filter, LLM triage, owner-only observe
3. **Merge:** combine observations from both passes, deduplicate by text containment
4. **Downstream:** label, theme, cluster, summarize, thesis, compose (unchanged)

### Cost

Multi-zoom observe costs ~$17 for a 430-conversation corpus. That's 2.7x baseline.
Downstream stages add ~$1.70 regardless of strategy. Total: ~$18.25.

The main cost lever is window size. Larger windows mean fewer observe calls but risk
reintroducing context rot. The current 8/4 split is untested against alternatives.

### What multi-zoom doesn't do

The two passes run independently. There is no cascade (triage informing windowed or
vice versa). The Orwell experiment suggested triaging first, then windowing the
reasoning passages. This is a potential optimization but adds complexity and the
independent merge already captures both zoom levels' signal.

### Deferred: observe from documents

The Orwell experiment was done outside the pipeline because the observe prompt expects
conversations, not essays. A "generate observations from document" capability would
let the pipeline learn from authored text (design docs, essays, blog posts) in addition
to conversations. Out of scope for now.
