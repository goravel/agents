---
name: Goravel Review Template
description: >
  Canonical output format and finding grammar for code reviews. Use when emitting, writing, or
  formatting a code review — defines the Summary/Verdict/Findings structure, Must Fix / Maybe
  Fix / Nits headings, the one-line anchor plus Problem/Impact/Fix shape, the two data shapes
  (NEW and NEW + ROUND), and the bar a comment finding must clear. Load via the skill tool before
  producing any review output.
---

# Review Output Format

Load this skill before emitting, writing, or formatting any code review. Follow the structure,
severity order, and finding grammar below exactly — consumers key on these exact strings.

## Template

# 🤖 Automated review
This is an AI-generated code review. Please double-check each finding before acting. You don't need to address every issue if they are inaccurate, but please point them out if any exist.
Please tick the checkboxes once you have completed the items.
<!-- round: 3 -->
<!-- reviewed-head: 9ab31e7c0d4e5f60a1b2c3d4e5f60718293a4b5c -->
Reviewed: 3d81f42..9ab31e7 — 2 new commits

## Summary
<1–2 plain-language sentences: what changed and the overall verdict.>

## Verdict
- [NEW] Must Fix: <N> · Maybe Fix: <N> · Nits: <N>
- [ROUND k](<url>) Must Fix: <N> · Maybe Fix: <N> · Nits: <N> · Resolved: <N>

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

### Maybe Fix
<!-- Maybe Fix still blocks: it must be fixed or carried forward, never shipped open. -->
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
- **NEW + ROUND** — the current finding set plus prior-round data: the current round number `N`,
  the full reviewed head SHA, the new-commit count (incremental) or changed-file count (full) for the
  `Reviewed:` line, and, per prior round, its `html_url`, per-severity open counts, and `Resolved` count.
  Use whenever the review is round-tracked, posted or not — the conversation copy carries the same
  markers and `[ROUND k]` lines. For an incremental round, also the reviewed range and `Resolved`
  limited to findings that range fixed.

## Rules

- **NEW shape.** Emit the `[NEW]` Verdict line and the findings. Omit the `<!-- round: N -->` and
  `<!-- reviewed-head: … -->` markers, the `Reviewed:` line, and every `[ROUND k]` line.
- **NEW + ROUND shape.** Emit the `<!-- round: N -->` marker and one `[ROUND k](<url>)` line per
  prior round, newest-first by comment timestamp.
- **State lines.** A NEW + ROUND body carries `<!-- round: N -->` and `<!-- reviewed-head: <sha> -->`
  on the two lines after the tick request; the marker holds the full 40-character SHA of the commit the
  round reviewed. A later run reads the newest marker and reviews only `<sha>..HEAD`. Never edit, drop,
  or reorder them — not when ticking, not when re-emitting.
- **Reviewed line.** One plain line under the markers: `<short base>..<short head> — <COMMITS> new commits`
  for an incremental round, `everything up to <short head> — <FILES> files` for a full round.
- **Carried findings.** In an incremental round a prior open finding in a file the delta still has,
  outside the lines it added or modified, is not resolved and not re-listed; it keeps its count on its
  `[ROUND k]` line. `Resolved: <N>` counts only findings the reviewed range fixed.
- **Folded Nits (both shapes).** Always wrap the entire `### Nits` block in `<details>` /
  `<summary>Nits (<N>)</summary>`: keep every numbered anchor line byte-for-byte and leave a blank
  line after `<summary>` and before `</details>`. Omit the whole block when there are no Nits.
- **Clean sentinel.** Emit exactly `✅ Review clean — no findings.`, followed by the Verdict
  line(s), when the NEW shape has zero findings, or the NEW + ROUND shape has zero findings and
  every `[ROUND k]` open count is zero.
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

## Comment findings

A comment earns its place only by explaining why, a non-obvious constraint, or a gotcha the code cannot
show. Flag a comment the diff adds or rewrites that restates the code, narrates a step, banners a
section, leaves commented-out code, or is unclear, too long, or not plain. Report each as Maybe Fix,
anchored on the comment line.

A comment that exists to explain a bad name means the name is wrong: flag the name on its own line under
the normal naming rule — or, if that line is not in the diff, flag the comment and say the name is the
problem. A doc comment opening with the name is not noise.
