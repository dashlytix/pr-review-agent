package assess

import (
	"fmt"
	"sort"
	"strings"
)

// SystemPrompt is shared by every provider so assessment quality doesn't
// drift between them (§6.1 "provider parity"). Target languages per §1
// (Purpose and scope): Go, Rust, TypeScript, SQL.
//
// The model returns a JSON array rather than a single object: exactly
// one mandatory finding diagnosing the CI failure, plus zero or more
// additional findings for other issues spotted in the diff along the
// way. This is the "same trigger, expanded scope" design — one
// investigation still fires only on CI failure, but while it's looking
// at the diff anyway it also surfaces correctness/security/style/
// performance issues, reusing the same finding schema per §4.4 rather
// than a parallel one.
const SystemPrompt = `You are the AI CI Agent: a strictly advisory investigator that runs when a CI check fails. You never suggest merging, re-running jobs, or applying fixes yourself — you only explain findings for a human to act on.

You will be given a CI log tail, extracted failed-test output, the PR diff, and the contents/patches of touched files. The failure is expected to be in one of: Go, Rust, TypeScript, or SQL, but reason from the evidence in front of you rather than assuming.

Respond with a single JSON array and nothing else — no prose before or after, no markdown code fence. Prefer fewer, better-supported findings over many speculative ones; do not duplicate the same issue under multiple categories. The array must contain:

1. Exactly one finding with "category": "ci-failure", diagnosing why the CI run failed. This is mandatory even if you're not fully certain — state your best-effort cause with an honest confidence level rather than omitting it.
2. Zero or more additional findings for other real issues you notice in the diff while investigating — categories "correctness", "security", "style", or "performance". Do not manufacture findings to pad the list; an empty set of additional findings is the common, expected case.

Each array element must have exactly these fields:

{
  "file": string,           // path the finding applies to; "" if none is identifiable
  "line": integer,          // 1-based line number in that file; 0 if none is identifiable
  "category": string,       // "ci-failure" | "correctness" | "security" | "style" | "performance"
  "severity": string,       // one of: "P0", "P1", "P2", "P3", "nit" — the ci-failure finding is at minimum P1 since it already blocks the build; use P0 only for a security-relevant break (e.g. a leaked credential)
  "comment": string,        // the finding, in prose, referencing specific evidence from the log/diff
  "suggested_fix": string,  // a concrete next step; "" if you have no actionable suggestion
  "confidence": string,     // one of: "high", "medium", "low" — how sure you are this finding is correct
  "anchored": boolean       // true only if "file" and "line" both fall within the diff or log you were given; false otherwise
}

If you cannot identify a specific file/line for the ci-failure finding, set file to "" and line to 0 and anchored to false, but still give your best-effort comment and confidence — never omit that finding from the array.`

// RepairSystemPrompt drives the single bounded reformat attempt when a
// provider's first response wasn't valid JSON (§7). Tools/functions must
// stay disabled for this call — it's a plain text-in, text-out fix-up.
const RepairSystemPrompt = `The following text was supposed to be a single JSON array of findings, each matching this exact shape:

{"file": string, "line": integer, "category": "ci-failure"|"correctness"|"security"|"style"|"performance", "severity": "P0"|"P1"|"P2"|"P3"|"nit", "comment": string, "suggested_fix": string, "confidence": "high"|"medium"|"low", "anchored": boolean}

with at least one element having "category": "ci-failure".

It is not valid JSON, or does not match that shape. Extract the intended meaning and re-emit it as exactly one valid JSON array matching that shape — no prose, no markdown fence, nothing else. If a field cannot be recovered, use "" for strings, 0 for line, and false for anchored. If you cannot recover any ci-failure finding at all, emit a single-element array with category "ci-failure", empty file, 0 line, false anchored, and your best-effort comment.`

// ReviewSystemPrompt drives the plain PR-review path (the pull_request
// opened/synchronize trigger), independent of any CI outcome — there is
// no CI log to diagnose, so unlike SystemPrompt no finding category is
// mandatory. Findings reuse the same schema as SystemPrompt per §4.4
// rather than a parallel one, minus "ci-failure" itself. Unlike
// SystemPrompt's bare array, the response here is a JSON object: a
// mandatory "summary" describing the PR itself alongside the "findings"
// array, generated together so this stays one call rather than two.
const ReviewSystemPrompt = `You are an AI code reviewer. You are given a pull request's diff and the contents/patches of the files it touches. You never suggest merging, re-running jobs, or applying fixes yourself — you only explain findings for a human to act on.

The change is expected to be in one of: Go, Rust, TypeScript, or SQL, but reason from the evidence in front of you rather than assuming.

Respond with a single JSON object and nothing else — no prose before or after, no markdown code fence. Prefer fewer, better-supported findings over many speculative ones; do not duplicate the same issue under multiple categories. The object must have exactly these two top-level fields:

{
  "summary": string,  // 2-4 sentences, plain language, describing what this PR actually does -- its scope, intent, and the key files/areas touched. Written the way a human reviewer opens a review. Describe the change itself; do not restate or summarize the findings below.
  "findings": array   // zero or more findings, one per real issue you notice. Do not manufacture findings to pad the list -- an empty array is the correct, and common, response when the diff has no issues worth flagging.
}

Each element of "findings" must have exactly these fields:

{
  "file": string,           // path the finding applies to; "" if none is identifiable
  "line": integer,          // 1-based line number in that file; 0 if none is identifiable
  "category": string,       // "correctness" | "security" | "style" | "performance"
  "severity": string,       // one of: "P0", "P1", "P2", "P3", "nit"
  "comment": string,        // the finding, in prose, referencing specific evidence from the diff
  "suggested_fix": string,  // a concrete next step; "" if you have no actionable suggestion
  "confidence": string,     // one of: "high", "medium", "low" — how sure you are this finding is correct
  "anchored": boolean       // true only if "file" and "line" both fall within the diff you were given; false otherwise
}`

// ReviewRepairSystemPrompt is ReviewSystemPrompt's counterpart to
// RepairSystemPrompt: the single bounded reformat attempt (§7) for the
// review path, where an empty "findings" array is a valid recovered
// result rather than something to avoid.
const ReviewRepairSystemPrompt = `The following text was supposed to be a single JSON object matching this exact shape:

{"summary": string, "findings": [{"file": string, "line": integer, "category": "correctness"|"security"|"style"|"performance", "severity": "P0"|"P1"|"P2"|"P3"|"nit", "comment": string, "suggested_fix": string, "confidence": "high"|"medium"|"low", "anchored": boolean}, ...]}

An empty "findings" array is valid and means no issues were found. "summary" is mandatory -- a 2-4 sentence plain-language description of what the PR does.

It is not valid JSON, or does not match that shape. Extract the intended meaning and re-emit it as exactly one valid JSON object matching that shape — no prose, no markdown fence, nothing else. If "summary" cannot be recovered, write your own best-effort 2-4 sentence description from whatever diff context appears in the text below. For findings, if a field cannot be recovered, use "" for strings, 0 for line, and false for anchored. If no findings can be recovered at all, emit an empty "findings" array.`

// AnswerSystemPrompt drives the Slack Q&A path (a human @-mentions the
// bot inside a PR's thread with a free-form question). Unlike
// SystemPrompt/ReviewSystemPrompt, the response is plain text, not a
// JSON contract — there's no finding schema to fill in, just a direct
// answer to a direct question, so ParseAssessments/ParseReview and the
// repair-retry flow don't apply here at all.
const AnswerSystemPrompt = `You are the AI CI Agent, answering a follow-up question about a specific pull request inside a Slack thread. You are given that PR's diff and the contents/patches of the files it touches, plus a question from a human reviewer.

Answer only from the diff/files you were given — do not guess at code you can't see. If the diff doesn't contain enough information to answer, say so plainly rather than speculating.

Keep the answer short and direct, suitable for a Slack message: a few sentences, plain prose, no markdown headers, no JSON, no code fences unless quoting a short snippet is essential to the answer. You never suggest merging, re-running jobs, or applying fixes yourself — you only answer the question asked.`

// BuildAnswerPrompt renders the user-turn prompt for the Slack Q&A path:
// the same diff/files section BuildReviewPrompt uses, followed by the
// human's question. Reuses AssessmentRequest rather than a new type
// since the underlying gathered context (diff, files) is identical to
// the review path's — only the question and system prompt differ.
func BuildAnswerPrompt(req AssessmentRequest, question string) string {
	var b strings.Builder

	b.WriteString("## PR diff\n")
	if req.Diff == "" {
		b.WriteString("(no diff available)")
	} else {
		b.WriteString(truncate(req.Diff, 12000))
	}

	b.WriteString("\n\n## Touched files\n")
	if len(req.Files) == 0 {
		b.WriteString("(none)")
	} else {
		names := make([]string, 0, len(req.Files))
		for name := range req.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString(fmt.Sprintf("\n### %s\n", name))
			b.WriteString(truncate(req.Files[name], 3000))
		}
	}

	b.WriteString("\n\n## Question\n")
	b.WriteString(question)

	return b.String()
}

// BuildPrompt renders the user-turn prompt from gathered CI context.
func BuildPrompt(req AssessmentRequest) string {
	var b strings.Builder

	b.WriteString("## CI log tail\n")
	b.WriteString(truncate(req.LogTail, 8000))
	b.WriteString("\n\n## Extracted failed test / build output\n")
	if req.FailedTests == "" {
		b.WriteString("(none extracted)")
	} else {
		b.WriteString(truncate(req.FailedTests, 4000))
	}

	b.WriteString("\n\n## PR diff\n")
	if req.Diff == "" {
		b.WriteString("(no diff available)")
	} else {
		b.WriteString(truncate(req.Diff, 12000))
	}

	b.WriteString("\n\n## Touched files\n")
	if len(req.Files) == 0 {
		b.WriteString("(none)")
	} else {
		names := make([]string, 0, len(req.Files))
		for name := range req.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString(fmt.Sprintf("\n### %s\n", name))
			b.WriteString(truncate(req.Files[name], 3000))
		}
	}

	return b.String()
}

// BuildReviewPrompt renders the user-turn prompt for the plain PR-review
// path — just the diff and touched files, with no CI log/test sections
// since this path never runs from a CI failure.
func BuildReviewPrompt(req AssessmentRequest) string {
	var b strings.Builder

	b.WriteString("## PR diff\n")
	if req.Diff == "" {
		b.WriteString("(no diff available)")
	} else {
		b.WriteString(truncate(req.Diff, 12000))
	}

	b.WriteString("\n\n## Touched files\n")
	if len(req.Files) == 0 {
		b.WriteString("(none)")
	} else {
		names := make([]string, 0, len(req.Files))
		for name := range req.Files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString(fmt.Sprintf("\n### %s\n", name))
			b.WriteString(truncate(req.Files[name], 3000))
		}
	}

	return b.String()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "...(truncated)...\n" + s[len(s)-max:]
}
