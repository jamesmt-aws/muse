# Editorial Composition

## Problem

A muse captures how the owner thinks. But the owner doesn't work in isolation. PRs go through
reviewers who care about specific things. Slack conversations happen with colleagues who have
strong positions. The owner benefits from anticipating these perspectives before engaging — not to
manipulate, but to stop wasting the other person's time on things the owner could have caught
themselves.

Today muse builds one profile: the owner's. The owner reads their muse and gets a mirror. What's
missing is the editorial board — the handful of people whose reactions the owner wants to
anticipate. A reviewer who always flags missing error handling. A colleague who pushes back on
unnecessary abstractions. A tech lead who cares about operational impact.

## Solution

### Peer muses

```bash
muse compose github/ellistarn                          # all of ellistarn's public activity
muse compose github/ellistarn --project karpenter      # scoped to karpenter repos
muse compose slack/@ellis --project karpenter          # scoped to karpenter-related channels
```

`muse compose github/ellistarn` builds a muse for `ellistarn` from their public GitHub activity.
The GitHub source searches for conversations involving `ellistarn`, assigns `role=user` to their
messages, and runs the standard observe/label/compose pipeline. The result is a muse.md that
captures how `ellistarn` reviews code — what they push back on, what they approve, what patterns
they flag.

`--project` narrows the scope. For GitHub, the project name is matched against repository names:
`--project karpenter` searches `involves:ellistarn repo:*/karpenter*`. A person's review style on
a scheduling system is different from their style on a CLI tool. Project scoping produces a more
focused muse.

For Slack, `--project` filters to channels whose name contains the project string. A reviewer who
is terse in `#karpenter-dev` and expansive in `#team-general` produces a different muse depending
on which channels are included.

The same pattern applies to Slack: `muse compose slack/@ellis` builds a muse from a colleague's
Slack activity in shared channels.

Peer muses are stored separately from the owner's muse, scoped by project when specified:

```
~/.muse/
├── muse.md                                    # owner's muse
├── peers/
│   ├── github-ellistarn/
│   │   └── karpenter/                         # project-scoped
│   │       ├── conversations/
│   │       ├── observations/
│   │       └── muse.md
│   ├── github-ellistarn/
│   │   └── default/                           # no project scope
│   │       ├── conversations/
│   │       ├── observations/
│   │       └── muse.md
│   └── github-jmdeal/
│       └── ...
```

### Identity pivot

The GitHub source today resolves the authenticated user's username and uses it for:

1. **Search**: `involves:{username}` finds conversations where the user participated.
2. **Role assignment**: Messages from `{username}` get `role=user`, everyone else gets `role=assistant`.
3. **Minimum participation**: Conversations with fewer than 2 owner messages are discarded.

For peer muses, the target username replaces the authenticated user in all three roles. The
authenticated user's GitHub token is still used for API access — the target doesn't need to
provide credentials since the data is public.

With `--project`, the search query adds a repo filter: `involves:{username} repo:*/{project}*`.
The glob allows matching across orgs — `--project karpenter` finds activity in
`aws/karpenter-provider-aws`, `kubernetes-sigs/karpenter`, and any fork. If the project name
contains a slash (`--project aws/karpenter`), it is used as an exact repo match.

Slack is similar: the target user ID replaces the authenticated user ID in activity search and role
assignment. With `--project`, only channels whose name contains the project string are included.
The authenticated user's token provides API access to shared channels. Private channels and DMs are
not accessible (the authenticated user only sees what they can see).

### Observe prompt

The existing human-conversation observe prompt works for peer muses. It already looks for positions
defended, pushback, architectural reasoning, and judgment — the same signals that matter in a
reviewer's muse.

The compose prompt needs a framing adjustment. The owner's muse prompt says "capture how they
think." A peer muse prompt should say "capture what this person consistently cares about when
reviewing code and engaging in technical discussion." The muse should be useful as a pre-flight
check: "would this person flag anything in my PR?"

### Using peer muses

```bash
muse ask --peer github/ellistarn --project karpenter "Review this PR: <paste diff>"
muse ask --peer github/jmdeal --project karpenter "What would you flag in this migration plan?"
```

`muse ask --peer` loads the peer's muse.md instead of the owner's. When `--project` is specified,
it loads the project-scoped muse. The system prompt frames the response as the peer's perspective:
"You are responding as {peer}, based on patterns observed in their code reviews and technical
discussions on {project}."

A multi-peer mode assembles several perspectives:

```bash
muse ask --peer github/ellistarn --peer github/jmdeal --project karpenter "Review this PR"
```

Each peer's muse weighs in independently. The owner sees which concerns are shared across reviewers
and which are specific to one person's priorities.

## Decisions

### Why public data only?

Peer muses are built from conversations the authenticated user can already see — public GitHub
activity and shared Slack channels. This is the same data a diligent colleague would read manually.
The muse automates the reading, not the access. No credentials from the target person are needed
or requested.

### Why separate storage?

Peer observations should not mix with the owner's. The owner's muse is built from conversations
where the owner's messages are the signal. A peer's muse is built from conversations where the
peer's messages are the signal. Mixing them produces a confused muse that represents neither
person.

### Why not embed source attribution in the muse?

A muse that says "I care about error handling because I reviewed PR #423 where it was missing"
leaks implementation details into what should be a distilled perspective. The muse represents
patterns, not provenance. Source conversations are in the peer's storage directory for anyone who
wants to trace a pattern back to its origin.

### Why project scoping?

A person reviews differently depending on context. An SRE reviewing a scheduler change focuses on
failure modes and blast radius. The same person reviewing a documentation PR focuses on accuracy
and completeness. A muse built from all of someone's GitHub activity averages across these
contexts, diluting the signal. Project scoping produces a muse that captures how the person thinks
about a specific codebase — the one the owner is about to submit a PR to.

Project scoping also bounds the data volume. A prolific reviewer with thousands of GitHub
conversations produces a noisy muse. Scoping to the relevant project keeps the observation set
focused and the compose cost reasonable.

### Why reuse the human-conversation observe prompt?

The reviewer signal — pushback, positions defended, patterns flagged — is exactly what the human
observe prompt already extracts. A reviewer-specific prompt would need to extract the same things
plus code-specific patterns (what they nitpick, what they let slide). Worth trying as a refinement
after the base case works.

## Deferred

### Reviewer-specific observe prompt

The human observe prompt extracts general peer interaction patterns. A reviewer-specific prompt
could focus on code review patterns: what triggers a "request changes," what gets approved without
comment, what style preferences recur. **Revisit when:** peer muses from the base prompt are
useful but miss code-specific patterns.

### Cross-muse discussion

Two peer muses discussing a design with each other, moderated by the owner's muse. The owner asks
a question, each peer muse responds, and the owner sees where they agree and disagree. **Revisit
when:** single-peer ask is validated and multi-perspective interaction is requested.

### Slack DM access

Slack peer muses are limited to shared channels. DMs between the owner and the target would be
high-signal (direct back-and-forth) but require the target's cooperation since DMs are private to
both participants. **Revisit when:** a peer explicitly opts in.
