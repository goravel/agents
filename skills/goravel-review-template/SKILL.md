---
name: goravel-review-template
description: >
  Canonical output format and finding grammar for code reviews. Use when emitting, writing, or
  formatting a code review — defines the Summary/Verdict/Findings structure, Must Fix / Should
  Fix / Nits headings, the one-line anchor plus Problem/Impact/Fix shape, and the two data shapes
  (NEW and NEW + ROUND). Load via the skill tool before producing any review output.
---

# Review Output Format

Load this skill before emitting, writing, or formatting any code review. Follow the structure,
severity order, and finding grammar below exactly — consumers key on these exact strings.

## Template

# 🤖 Automated review
This is an AI-generated code review. Please double-check each finding before acting. You don't need to address every issue if they are inaccurate, but please point them out if any exist.
<!-- round: N -->

## Summary
<1–2 plain-language sentences: what changed and the overall verdict.>

## Verdict
- [NEW] Must Fix: <N> · Should Fix: <N> · Nits: <N>
- [ROUND k](<url>) Must Fix: <N> · Should Fix: <N> · Nits: <N> · Resolved: <N>

## Findings

### Must Fix
1. [ ] `path/file.ext:LINE` **Short plain-language title**
   **Problem:** <one short sentence — the defect, in everyday words>
   **Impact:** <one short sentence — the concrete consequence>
   **Fix:** <one short sentence — the concrete change>
   ```go
   // before
   <current buggy code>
   // after
   <corrected code>
   ```

### Should Fix
1. [ ] ...

<details>
<summary>Nits (<N>)</summary>

### Nits
1. [ ] `path/file.ext:LINE` **Short plain-language title**
   **Problem:** <...>
   **Impact:** <...>
   **Fix:** <...>
   ```go
   // before
   <...>
   // after
   <...>
   ```

</details>

## Inputs

The review agent supplies one of two data shapes. Apply the template above to whichever it is:

- **NEW** — the current finding set only. Use when the output is not round-tracked (a local
  artifact, a findings-only handoff).
- **NEW + ROUND** — the current finding set plus prior-round data: the current round number `N`
  and, per prior round, its `html_url`, per-severity open counts, and `Resolved` count. Use when
  the output is posted as a round-tracked PR review comment.

## Rules

- **NEW shape.** Emit the `[NEW]` Verdict line and the findings. Omit the `<!-- round: N -->`
  marker and every `[ROUND k]` line.
- **NEW + ROUND shape.** Emit the `<!-- round: N -->` marker and one `[ROUND k](<url>)` line per
  prior round, newest-first by comment timestamp.
- **Folded Nits (both shapes).** Always wrap the entire `### Nits` block in `<details>` /
  `<summary>Nits (<N>)</summary>`: keep every numbered anchor line byte-for-byte and leave a blank
  line after `<summary>` and before `</details>`. Omit the whole block when there are no Nits.
- **Clean sentinel.** Emit exactly `✅ Review clean — no findings.`, followed by the Verdict
  line(s), when the NEW shape has zero findings, or the NEW + ROUND shape has zero findings and
  every `[ROUND k]` open count is zero. This string keys the implement orchestrator's convergence
  check.
- The anchor line (`file:line` + **Title**) is the merge/dedupe key and is mandatory; the description
  lines and example block are ignored for matching.
- **Scope.** A finding must be anchored to a line the reviewed diff adds or modifies (a `+` line),
  within one line of such a line. Drop pre-existing issues in unchanged code, findings in files
  the diff does not touch, and unrelated refactors or "while here" suggestions. When a change
  affects unchanged code, anchor the finding to the changed line and explain the consequence
  there. The review agent applies a final added-line filter (±1 line) after merging, so an
  unanchored or out-of-scope finding is discarded silently.
- Write each finding in plain language for a busy developer who did not write the code:
  - Title = a short noun phrase (≤8 words), no jargon — e.g. "Shared slice returned to caller".
  - One problem per finding; never bundle two issues.
  - One short sentence per line: Problem, then Impact, then Fix.
  - Avoid "utilize", "leverage", "aforementioned", "hereby".
  - Include the before/after block whenever the finding concerns code behavior (logic, safety,
    performance, tests); omit only when no meaningful snippet exists (naming, docs, layout).
  - The indented example block: one fence max, ≤8 lines, no nested fences, `// before` / `// after`
    (`#` for Python), with a language tag matching the file.
- Omit empty severity headings entirely.
