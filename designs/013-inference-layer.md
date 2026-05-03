# Inference Layer

## Problem

Per-stage callers across the compose pipelines need to send LLM requests with budget
constraints and optionally extended thinking. Three providers (Anthropic, Bedrock, OpenAI)
implement these in different native APIs. The current implementation works but is
undocumented: per-stage budget choices, the `MaxTokens + ThinkingBudget` summing convention,
and the gap with thinking-by-default models all live only in code. Without a documented
contract, future changes either re-discover the implicit invariants or accidentally violate
them. This is what happened to the label step: a `WithMaxTokens(1024)` budget worked silently
on non-thinking models and broke loudly on Qwen3 served via local llama.cpp, because Qwen3's
chat template defaults thinking on regardless of OpenAI-compatible flags.

## Solution

### Two-axis contract

`MaxTokens` is the output budget. `ThinkingBudget` is the thinking budget. They are separate
fields on `inference.ConverseOptions` and serve different goals. `MaxTokens` caps the
user-visible content; `ThinkingBudget` allocates non-visible reasoning room. Provider clients
translate the pair to their native APIs, summing them when constructing the maximum-token cap
so the call has room for both.

Defaults live in `internal/inference/usage.go`:

- `DefaultMaxTokens = 4096` — output ceiling, picked to prevent runaway generation.
  Inherited from the Vercel AI SDK's default for Claude on Bedrock (PR #12, Mar 2026).
- `DefaultThinkingBudget = 16384` — extended-thinking budget, picked to give models reasoning
  room. Was 16000 in PR #161 (Apr 2026), rounded to a power of two for taste; no behavioral
  change at scale.

The two values are deliberately not ratiometric. They were picked separately, three weeks
apart, for unrelated reasons. Output budget addresses runaway generation; thinking budget
addresses reasoning headroom. Reading them together as a "ratio" is a category error and
should not be done in future tuning.

Defaults trigger when callers omit the option:

- `WithMaxTokens()` not called → use `DefaultMaxTokens`.
- `WithThinking()` not called → no extended thinking requested. Default is no thinking, not
  some thinking.

### Per-provider mapping

All three providers implement the same summing pattern. The shape:

```
maxTokens = opts.MaxTokens (or DefaultMaxTokens) + opts.ThinkingBudget
```

is sent as the provider's max-tokens cap. Whether thinking actually counts against that cap
varies by provider and model, but reserving room for both is the simplest portable invariant.

**Anthropic** (`internal/anthropic/client.go`). Sums into `params.MaxTokens`. Sets
`params.Thinking = ThinkingConfigParamOfEnabled(ThinkingBudget)` only when
`ThinkingBudget > 0`. Without the explicit Thinking config, no extended thinking happens.

**Bedrock** (`internal/bedrock/client.go`). Same summing into `aws.Int32(maxTokens)`. Sets
`additionalModelRequestFields.thinking = {effort: ...}` mapped from the budget via
`effortForBudget()`. Same gating: no thinking config, no thinking.

**OpenAI** (`internal/openai/client.go`). Sums into `params.MaxCompletionTokens` (the newer
field; `MaxTokens` is deprecated for chat completions). Extended thinking only triggers for
o-series reasoning models (`isReasoningModel(c.model)`):

```
if opts.ThinkingBudget > 0 && isReasoningModel(c.model):
    params.ReasoningEffort = reasoningEffortForBudget(opts.ThinkingBudget)
```

For non-reasoning OpenAI models, `ThinkingBudget` is silently ignored. The summed budget
still applies, so callers always get the budget they asked for; they just may not get the
reasoning effort.

### The thinking-by-default gap

Some models think regardless of caller intent. Two variants matter:

**Local Qwen3 Thinking via llama.cpp.** The model emits `<think>...</think>` before the answer.
The OpenAI-compatible server ignores `reasoning_effort` (the model isn't OpenAI's o-series). A
caller invoking `WithMaxTokens(1024)` without `WithThinking()` expects 1024 tokens of content;
the model spends those tokens thinking, the answer never emits, and the call returns truncated
(`finish_reason=length`, content empty, reasoning_content full). Empirically reproduced on
Qwen3-30B-A3B-Thinking-2507 with the muse label-batch prompt.

**OpenAI o-series called without `WithThinking()`.** The model reasons by default at minimum
effort. Less acute because OpenAI's API accommodates the default within typical budgets, but
it's the same shape: the caller's `MaxTokens` budget gets shared with reasoning the caller
didn't ask for.

The asymmetry vs Bedrock: with Anthropic on Bedrock, thinking is opt-in (`WithThinking(N)`
sets the thinking budget; provider sums it into the wire-level cap). The caller controls.
With local thinking-by-default models, the model decides; the caller has no signal to
allocate room. The right place for the fix is therefore deployment-level, not per-call.

### Workaround: deployment-level thinking budget

The OpenAI provider gains a mutator on the muse client:

```go
// SetThinkingByDefault registers a per-call token allowance for deployments that
// serve thinking-by-default models (e.g. Qwen3 Thinking via local llama.cpp).
// The budget is added to MaxCompletionTokens on every request, on top of the
// caller's WithMaxTokens and any WithThinking allocation.
func (c *Client) SetThinkingByDefault(budget int32)
```

Construction sites populate it from configuration. `cmd/root.go` reads
`MUSE_OPENAI_THINKING_BUDGET` (an integer) and applies it after `NewClient`:

```
MUSE_OPENAI_THINKING_BUDGET=4096
```

The budget is deployment configuration, not per-invocation. It lives alongside
`OPENAI_BASE_URL` and `OPENAI_API_KEY` in the shell-rc activation script. Setting to 0 (or
leaving unset) is the right answer for hosted deployments where models opt in to thinking via
`WithThinking()`.

The math, post-fix:

```
maxTokens = (caller's MaxTokens or DefaultMaxTokens)
          + (caller's ThinkingBudget if reasoning model)
          + (deployment's thinkingByDefault)
```

Composition with `WithThinking()`: additive. If a caller invokes `WithThinking(N)` against an
o-series deployment that also has `thinkingByDefault = M` set, the wire cap is
`MaxTokens + N + M`. The deployment's allocation is the floor; the caller's request adds on
top. Same as the Bedrock summing pattern, just with two terms instead of one.

The o-series case is left as a known limitation. The OpenAI API doesn't expose a "no
reasoning" toggle; reasoning models always reason at some level. The minimum effort is the
floor. Setting `thinkingByDefault` for an o-series deployment is permissible (it just makes
the budget more generous than necessary).

### Per-stage budget policy

Per-stage `MaxTokens` should reflect the expected *content* output. The label step's expected
output is ~10 short labels (~100 tokens), so `WithMaxTokens(1024)` is the right call-level
budget. Other stages with longer output use 4096 because their content is meaningfully larger,
not because of thinking concerns.

Thinking allocation is a separate concern handled by the inference layer, either by caller
intent (`WithThinking`) or deployment configuration (`SetThinkingByDefault`). Per-stage
budgets do not need to be padded for thinking; that mixes two concerns and creates an
ambiguous floor.

### Caching

Inference budgets do not appear in cache fingerprints (see `004-clustered-compose.md`,
"Caching"). The compose pipelines fingerprint observations on prompt content and timestamps,
not on `MaxTokens` or `ThinkingBudget`. The same model, prompt, and input typically produce
equivalent observations regardless of headroom, as long as truncation does not occur. If
truncation occurs, the truncated observation is stored and future runs with a larger budget
will pull from cache. The workaround is `--reobserve` after raising budgets. This is a known
limitation, not a goal.

### Telemetry

`Usage` (in `internal/inference/usage.go`) tracks `InputTokens`, `OutputTokens`, and `cost`
per call. Providers populate from their native usage objects. Cost is computed via per-model
`Pricing` (input/output per-token rates). Local providers (llama.cpp via OpenAI base URL)
report tokens but no pricing entry, yielding $0.00 cost. This is intentional: local inference
has no per-token billing.

## Decisions

### Why separate `MaxTokens` and `ThinkingBudget` rather than a single budget?

The two serve different goals. Output budget caps user-visible content; thinking budget
allocates non-visible reasoning room. Conflating them would force callers to encode "I need
4k of output but the model might think for 16k" as a single number, which is harder to reason
about and harder to tune per-provider (since some providers count thinking against the same
cap and some don't).

### Why provider-side summing?

Anthropic, Bedrock, and OpenAI's reasoning models all expose a single max-tokens cap that
applies to thinking plus content. Summing on the muse side at construction time means
callers don't need to know which provider is in use. The provider knows whether thinking
actually counts against the cap; the simplest portable invariant is "always reserve room for
both."

### Why deployment-level rather than per-stage budget bumps?

Earlier in the work we tried bumping the label step's `MaxTokens` from 1024 to 4096 to
"absorb thinking." That works mechanically but mixes two concerns at the wrong layer: the
label step's intent is "1024 tokens of labels," and the thinking is a property of the
deployment, not the step. Padding every per-stage budget for a deployment-level concern is a
leaky abstraction: each new short-output stage has to remember the cushion, and stages keep
the cushion when running against non-thinking deployments where it wastes budget.
`SetThinkingByDefault` lets per-stage budgets stay aligned with content intent.

### Why an env var rather than a CLI flag?

`MUSE_OPENAI_THINKING_BUDGET` is deployment configuration, not per-invocation. Setting it in
a shell-rc-loaded env var is closer in spirit to `OPENAI_BASE_URL` (also an env var, also
deployment config). A CLI flag would imply per-call control that callers don't actually need.

### Why a mutator method instead of a `NewClient` option?

`NewClient` already takes `option.RequestOption ...` from the openai-go SDK. Mixing
muse-side options with SDK options through a unified interface is more code than the use
case justifies. A `SetThinkingByDefault(int32)` method on the constructed client is one
line at the call site (after `NewClient`) and doesn't change the existing constructor
signature.

## Deferred

### Disabling thinking via `chat_template_kwargs.enable_thinking`

Some Qwen3 chat templates (e.g. the general Qwen3-30B-A3B-Instruct) gate thinking on the
Jinja variable `enable_thinking`. For those templates, setting `enable_thinking=false`
suppresses thinking entirely, which is cleaner than allocating extra budget. Add it when we
need it.

### Automatic per-stage budget calibration

The current per-stage budget choices are manual constants. A future iteration might compute
budgets from input characteristics or measured truncation rates. Out of scope; the manual
per-stage budgets plus the deployment-level thinking allowance handle the cases we have.

### Cost reporting for local providers

Local inference reports $0.00 because there's no per-token billing. A future iteration
could attribute compute cost (electricity, time, depreciation) to local calls for
end-to-end cost comparisons. Out of scope and probably not worth doing; local cost is
empirically zero for personal use.
