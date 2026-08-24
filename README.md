# ai-ci-agent

Implementation of `ADR-001` / the accompanying Tech Spec (`AI_CI_Agent_ADR_TechSpec.docx`): a stateless GitHub Action that investigates a CI failure and posts findings as a PR comment. No database, no separate service — every invocation re-derives its context from the GitHub API (§3).

**CI completion is the single entry point, with two outcomes.** The `workflow_run` trigger fires once the `CI` workflow completes, and dispatches on its conclusion:

- **Failure** → the full pipeline: `gather.Gather` → `Provider.Assess` → `post.RenderAssessments`/`post.Post`. Findings are an array rather than a single one: exactly one mandatory `ci-failure` diagnosis, plus zero or more additional `correctness`/`security`/`style`/`performance` findings spotted in the same diff while investigating.
- **Success** → the LLM is never called (no `Provider.Assess`, no API cost) — just a short templated `post.RenderPass`/`post.PostPass` comment acknowledging the pass.

A separate `on: pull_request` (`opened`/`closed`) trigger is unrelated to review: it only drives Slack lifecycle notifications (see "Slack notifications" below). A prior revision (see git history, PR #8) ran a review independent of CI status on every `opened`/`synchronize` event; that trigger has been reverted so CI status gates the review pipeline again, per the original ADR-001 §1 design.

## Layout vs. the spec

| Path | Spec section | What's here |
|---|---|---|
| `action.yml` | §4.1 | Action definition — `llm-provider` (default `claude`), `llm-api-key`, `llm-base-url`/`llm-model` for pointing a provider at an API-compatible gateway, plus a `github-token` input the spec's snippet didn't spell out but the Action needs to read context and post comments |
| `Dockerfile` | §2 | Multi-stage build, stdlib-only Go binary on a distroless base |
| `cmd/agent/main.go` | §5, §7 | Orchestration: gather → assess → post (`investigate`) on CI failure, the templated `postPassComment` on CI success, the schedule-triggered reconciliation sweep (§7), and Slack lifecycle notifications (`handlePullRequestEvent`) for the `pull_request` opened/closed trigger |
| `internal/gather` | §2.1, §3 | GitHub API calls for the log tail, PR diff, touched files; language-aware failure-line extraction (Go/Rust/TS/SQL, §1). `GatherForReview` (no log tail, for a CI-independent review) is currently unused by `main.go` since the review trigger was reverted to gate on CI status — kept in place rather than deleted, in case that entry point returns |
| `internal/provider` | §4.2 | `Provider` interface (`Assess` and `Review` both return `[]Assessment`), `ClaudeProvider`, `OpenAIProvider`. `Review` has no current caller in `main.go`, same as `GatherForReview` above |
| `internal/assess` | §4.2, §6.1 | Prompt building, JSON array parsing + one bounded repair attempt, diff-anchor validation — all applied per-finding; `Review*`-prefixed prompt/parse functions served the reverted CI-independent review path and are currently uncalled |
| `internal/post` | §4.3, §4.4, §6.3 | Renders every finding into one comment (primary `ci-failure` finding first, any extras in an "Other findings" section), marker-based idempotency, stale-head handling; `RenderPass`/`PostPass` post the CI-success path's short templated comment under its own `passMarker`. `RenderReviewFindings`/`PostReview`/`reviewMarker` served the reverted review path and are currently uncalled |
| `internal/ghclient` | — | Shared GitHub REST client with retry/backoff on rate limiting (not named as its own package in the spec, but needed by both `gather` and `post`) |
| `eval/` | §9 | Evaluation harness — 20 fixtures across the four target languages and a scoring CLI (scores the mandatory `ci-failure` finding against each fixture's known answer) |

## Why `assess` and `provider` are split the way they are

The spec's §4.2 code block shows `AssessmentRequest`/`Assessment`/`Provider` all declared together, with a note that "prompt building, JSON parsing/repair" belongs to `internal/assess`. Taken literally, that's a cycle: both `ClaudeProvider` and `OpenAIProvider` need to call `assess.BuildPrompt`/`assess.ParseAssessments` (so provider depends on assess), but the shared types are used by both. The types now live in `internal/assess`, and `internal/provider` re-exports them as type aliases (`type Assessment = assess.Assessment`), so calling code still writes `provider.Assessment` per the spec while the prompt/parse logic isn't duplicated between the two providers.

## Why findings are one array instead of a fixed-category single object

`Assessment.Category` was always documented as extensible (§4.4: "extends the review agent's existing category set (correctness, style, security, …)") — the multi-finding array is that extension actually implemented, rather than adding a second, parallel finding type. `assess.ParseAssessments` enforces exactly one `ci-failure` entry is present (the mandatory diagnosis every run produces) and validates every entry's category against `assess.ValidCategories`; `assess.ValidateAnchors` applies the same §6.1 diff-anchor guardrail to each finding independently, computing the changed-line map once rather than per finding. `post.RenderAssessments` always renders the `ci-failure` finding first and groups anything else under "Other findings" — one comment per run regardless of how many findings it carries.

## Guardrails implemented

- **§6.1 diff-anchored findings** — `assess.ValidateAnchor` parses the captured unified diff (and per-file patches) into actual changed-line sets and downgrades `anchored` to `false` if the model's file/line claim doesn't fall inside them.
- **§6.1 posting authority before untrusted content** — the GitHub token's scope is fixed by the workflow before any log/diff/file content (all contributor-influenceable) is ever read; nothing in that content can grant itself posting authority.
- **§6.3 idempotency** — `post.Post` looks for a hidden `<!-- ai-ci-agent:marker:sha=... -->` comment before posting; no table, no database.
- **§6.3 stale-head handling** — the PR's current head is re-checked right before posting; if it moved, the comment is posted body-only with both SHAs called out.
- **§7 failure modes** — provider timeout → fallback comment linking raw logs; GitHub rate limiting → retried with backoff, then a minimal comment; malformed JSON → one bounded repair call (tools/system prompt only, no fresh context), then a minimal comment; missing provider config → fails fast instead of silently degrading.
- **§7 reconciliation backstop** — `GITHUB_EVENT_NAME=schedule` triggers `reconcile()`, which sweeps recent failed runs for any missing a marker comment (dropped-webhook case).

## Pointing a provider at a gateway

`llm-base-url` and `llm-model` let either provider talk to an API-compatible endpoint instead of the vendor's own API — OpenRouter, a self-hosted proxy, or a managed gateway that injects credentials at the network edge. `llm-provider` still selects the *wire format* (`claude` → Anthropic Messages, `openai` → chat completions); the base URL only changes where that format is sent.

```yaml
- uses: ./
  with:
    llm-provider: claude
    llm-api-key: ${{ secrets.LLM_API_KEY }}
    llm-base-url: https://my-gateway.internal
    llm-model: claude-opus-5
```

The base URL may be a bare host, a `/v1` prefix, or a fully qualified endpoint — `provider.resolveEndpoint` appends the provider's canonical path only if it isn't already there, so gateway docs can be pasted as written instead of 404-ing on a doubled `/v1`.

Configuration precedence, highest first: action inputs (`llm-base-url`/`llm-model`) → provider-specific env vars (`ANTHROPIC_BASE_URL`/`OPENAI_BASE_URL`, and the `_MODEL` equivalents) → provider-neutral `LLM_BASE_URL`/`LLM_MODEL` → the provider's built-in default. Inputs deliberately win over the environment: a workflow that names its gateway explicitly shouldn't be silently redirected by an ambient variable on the runner. The neutral pair exists for gateways that serve both API shapes at one host, so switching `llm-provider` needs no other change.

`provider.New` takes an explicit `Config` and reads no environment itself; `provider.ConfigFromEnv` does the env resolution. Keeping those separate is what makes the precedence above testable in one place rather than scattered across constructors.

The GitHub API host follows `GITHUB_API_URL`, which the Actions runner sets on every job — so the same image works against GitHub Enterprise, and an end-to-end run can be pointed at a stub server.

## Building

No third-party Go modules are used (stdlib only), so this builds offline once a Go toolchain is available:

```
go build ./...
```

## Running the eval harness (§9)

```
LLM_API_KEY=sk-... go run ./eval/cmd/evalrun -provider claude -min-score 0.75 -verbose
```

Also works against any API-compatible gateway via env overrides, so you're not limited to a raw provider key. Both providers accept a base-URL override (`ANTHROPIC_BASE_URL` / `OPENAI_BASE_URL`) and a model override (`ANTHROPIC_MODEL` / `OPENAI_MODEL`):

```
LLM_API_KEY=sk-or-... OPENAI_BASE_URL=https://openrouter.ai/api/v1 OPENAI_MODEL=openai/gpt-4o-mini \
  go run ./eval/cmd/evalrun -provider openai -min-score 0.75 -verbose
```

The base URL may be a bare host, a `/v1` prefix, or the fully qualified endpoint — `provider.resolveEndpoint` appends the provider's canonical path only if it isn't already there, so gateway docs can be copy-pasted as written instead of 404-ing on a doubled `/v1`.

### Against a keyless gateway (exe.dev)

The same overrides point either provider at the exe.dev managed LLM gateway, where credentials are injected at the network edge and `LLM_API_KEY` is just a non-empty placeholder:

```
# Anthropic-shaped path
LLM_API_KEY=implicit ANTHROPIC_BASE_URL=https://llm.int.exe.xyz ANTHROPIC_MODEL=claude-opus-5 \
  go run ./eval/cmd/evalrun -provider claude

# OpenAI-shaped path
LLM_API_KEY=implicit OPENAI_BASE_URL=https://llm.int.exe.xyz OPENAI_MODEL=gpt-5.6-sol \
  go run ./eval/cmd/evalrun -provider openai
```

`eval/fixtures/` has 20 fixtures (5 per target language: Go, Rust, TypeScript, SQL), within §9's 20-30 target. Live runs through that gateway: `claude-opus-5` 20/20 and `gpt-5.6-sol` 19/20 on cause-match, both 20/20 on severity and anchor validity. Encouraging, but still one run per model — not a trend line. `gpt-5.6-sol`'s single miss was `go-004-index-out-of-range`.

The first Claude run through that gateway also surfaced a real bug rather than a scoring result: 15/20 fixtures errored because `ClaudeProvider` read `content[0].text`, and reasoning-capable models put a `thinking` block there. The empty string failed to parse, then got sent as the repair call's user message, which the Messages API rejects with a 400 — so a decode bug surfaced as a confusing "malformed after repair" error. `claudeResponse.Text()` now concatenates every `text` block and skips the rest, and a no-text response fails immediately instead of triggering a repair call it knows will 400.

## Slack notifications

Optional, opt-in via the `slack-webhook-url` input (a Slack incoming webhook URL) — if
unset, none of this fires and the rest of the action behaves exactly as documented above.
`internal/notify` posts a Slack Block Kit message using a legacy attachment (so the colored
left border renders), no retry, for four lifecycle events only — never review content,
findings, or diagnosis text, which stay in the GitHub PR comment posted separately:

- **PR opened** — blue border.
- **PR merged** — green border, plus a Commit field (short SHA + base branch).
- **PR closed without merging** — red border.
- **CI check failed** — orange border, on the `workflow_run` failure path (right after
  `investigate()` posts a fresh, not-already-posted PR comment). No card is sent for a
  passing CI run's templated comment.

Each card is a single attachment: a header/subtitle section, a Status field (plus Commit for
the merged case), and a "View PR" button linking to the PR's `html_url`.

A consuming workflow needs a second trigger alongside the existing `workflow_run`/
`schedule` ones to get the PR-lifecycle notifications:

```yaml
on:
  pull_request:
    types: [opened, closed]

jobs:
  pull-request:
    runs-on: ubuntu-latest
    steps:
      - uses: dimension/ai-ci-agent@v1
        with:
          github-token: ${{ github.token }}
          llm-provider: claude
          llm-api-key: ${{ secrets.LLM_API_KEY }}
          slack-webhook-url: ${{ secrets.SLACK_WEBHOOK_URL }}
```

`llm-provider`/`llm-api-key` are still required inputs on `action.yml`, but this job's code
path never reaches the LLM — `pull_request` only ever drives the Slack lifecycle ping.

## Open items carried over from §11

These are the spec's own open questions, unresolved here too:

- Comment surface is implemented as a PR comment per the §1.1 assumption; job-summary/check-run-annotation would only touch `internal/post` and the render calls in `cmd/agent/main.go`.
- Pilot repo, target eval score, eval-dataset ownership/cadence, and per-provider cost ceiling are still unset — they're policy decisions, not code.
- Provider selection here is a fixed `llm-provider` input, not auto-detected from which API key is present. Note this selects the wire format, not the vendor: with `llm-base-url` set, `llm-provider: openai` reaches any chat-completions-compatible endpoint.
- End-to-end verification is manual, not a checked-in test: the agent binary was run against a local GitHub API stub (via `GITHUB_API_URL`) with a live LLM behind `llm-base-url`, and both providers produced a correct nil-guard-removal diagnosis. Worth turning into a fixture-driven integration test with a recorded provider response.
