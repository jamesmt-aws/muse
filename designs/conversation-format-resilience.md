# Conversation Format Resilience

## Problem

Muse reads conversations written by external tools (Claude Code, Kiro, OpenCode). Those tools
control their own JSON schema and change it without notice. Muse's conversation struct requires
specific field names (`conversation_id`, `source`). When an upstream tool renames a field — as
Claude Code did with `session_id` -> `conversation_id` — every conversation written under the old
schema becomes unreadable.

This isn't a one-time migration problem. It will recur: upstream tools ship new formats, users
accumulate conversations across versions, and `--reobserve` re-reads everything from disk. The
current behavior is fatal: one unreadable file kills the entire observe pipeline. In the eval run,
~100 of 234 claude-code conversations failed validation, blocking all observation.

The failure mode is especially bad because it's silent until triggered. Normal runs skip
already-observed conversations (cache hit). The problem only surfaces on `--reobserve`, on first
run against old data, or when a new sandbox has no cached observations — exactly the cases where
the user expects things to work.

## Proposed Changes

### 1. Accept both field names during deserialization

The `Conversation` struct should accept `session_id` as an alias for `conversation_id`. This is a
one-line change (custom UnmarshalJSON or a second struct tag via a helper). The validation error
disappears for existing data.

More generally: when a field is renamed upstream, add backward-compatible deserialization rather
than requiring users to re-export their data.

### 2. Skip invalid conversations instead of failing

When a conversation fails validation during observe, log a warning and continue. The user sees
"Skipped 3 invalid conversations" at the end instead of a fatal error. This matches the principle
that distillation is best-effort over the available data — one bad file shouldn't block insights
from the other 200.

Implementation: in the observe goroutine, when `GetConversation` returns a validation error,
increment a skip counter and continue instead of setting `firstErr`.

### 3. Report skipped conversations

Add a `Skipped int` field to `compose.Result` so the output shows:
```
Processed 97 conversations (3 skipped, 130 pruned)
```

Users can then investigate the skipped files if they care, but the pipeline isn't blocked.

## What this doesn't address

- **Forward migration**: converting old-format files to new format on disk. Probably not worth it —
  accept both formats indefinitely.
- **Validation for other fields**: `source` is also required but less likely to be missing. Same
  skip-and-warn pattern would apply.
- **Schema versioning**: a `schema_version` field exists but isn't used for dispatch. If more format
  changes accumulate, a proper version-dispatch deserializer may be needed.
