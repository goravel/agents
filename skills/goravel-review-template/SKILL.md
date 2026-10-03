---
name: goravel-review-template
description: >
  Canonical output format and finding grammar for code reviews. Use when emitting, writing, or
  formatting a code review — defines the Summary/Verdict/Findings structure, Must Fix / Should
  Fix / Nits headings, the one-line anchor plus Problem/Impact/Fix shape, and the PRIMARY vs
  SUBAGENT variants. Load via the skill tool before producing any review output.
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

### Nits
1. [ ] ...

## Variants

- **PRIMARY** (full, round-tracked reviews): emit the `<!-- round: N -->` marker, the `[NEW]` line,
  and one `[ROUND k](<url>)` line per prior round (newest-first by comment timestamp). List NEW
  findings only.
- **SUBAGENT** (findings-only / aggregated output): omit the round marker, the `[NEW]` line, and the
  `[ROUND k]` lines; show only the plain line `- Must Fix: <N> · Should Fix: <N> · Nits: <N>`.
  List ALL current findings.
- **Clean sentinel**: zero findings (PRIMARY: also every `[ROUND k]` open count zero) → emit exactly
  `✅ Review clean — no findings.`, followed by the Verdict line(s). The convergence check keys on
  this exact string.

## Rules

- The anchor line (`file:line` + **Title**) is the merge/dedupe key and is mandatory; the description
  lines and example block are ignored for matching.
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
