---
name: Goravel PR
description: Create or update the pull request for the current branch and enforce a strict PR body format. Use this skill whenever the user asks to create a PR, update PR description, sync PR text with latest branch logic, or asks for Summary/Why style PR content.
---

# Create Or Update PR

## Purpose

Create a PR for the current branch when none exists, otherwise update the existing PR description from the latest diff.

---

## PR Body Specification

### Structure

The PR body must contain exactly these two top-level sections, in this order:

- `## Summary`
- `## Why`

No additional top-level sections are permitted.

### Template

Fill in one `## Why` body — the feature block or the bug-fix block, never both.

````markdown
## Summary
- <behavior bullet 1>
- <behavior bullet 2>
- <optional behavior bullet 3>

<optional: Closes https://github.com/goravel/goravel/issues/1234>

## Why
<1–2 short paragraphs about what changed and why it matters.>

<!-- feature: exactly one block, end-user usage -->
```go
<real user-facing code, written in the style of https://github.com/goravel/example>
```

<!-- bug fix: one block per bug, before and after inside one fence -->
```go
// Before: <what was wrong>
<real user-facing code before the fix>

// After: <the fix>
<real user-facing code after the fix>
```
````

### Summary Rules

- Include exactly 2 to 3 bullet points.
- Bullets must describe behavior changes, not file-by-file edits.
- If an issue number is detected, append a standalone closing line after the bullets:
  `Closes https://github.com/goravel/goravel/issues/<number>`

### Why Rules

**All PR types**
- Include 1 to 2 short paragraphs explaining what changed and why.
- Code blocks must contain only real user-facing code; never include implementation snippets, placeholder markers, or pseudo-code inside code fences.

**Feature PRs**
- Include exactly one fenced code block written in the style of https://github.com/goravel/example.
- The block must reflect end-user usage semantics, not internal implementation detail.
- Model the snippet after the closest matching pattern in the example repository (controllers, routes, models, etc.).

**Bug-fix PRs**
- Include one fenced code block per distinct bug, with `// Before:` and `// After:` inside it; the prose says what was wrong and why it matters.

**Mixed PRs (feature + bug fix)**
- Use bug-fix formatting when multiple bug fixes are present; otherwise use feature formatting.

---

## Code Style Reference

Write user-facing blocks the way `goravel/example` writes them — open that repository and copy the closest
pattern instead of inventing one:

- routes: `facades.Route()`, with `middleware.*` and `route.Router` groups
- input: `ctx.Request().Validate(...)`, then `validator.Fails()` / `validator.Bind(...)`
- data: `facades.Orm().Query()`
- auth: `facades.Auth(ctx).LoginUsingID(...)`

```go
facades.Route().Middleware(middleware.Jwt()).Get("users", userController.Index)
```

---

## Workflow

1. **Detect branch**
   - Run `git branch --show-current` → `branch`.
   - If empty, stop and report failure.
   - If the branch is the default (`master` / `main`), create a feature branch first: `git checkout -b feat/<short-description>`, then set `branch` to that name.

2. **Detect issue number**
   - Only two branch shapes carry an issue: `issue-<n>-<TS>` (a run started from an issue link) and a head
     branch shaped `<type>/<n>-<description>`, `<n>` 1–5 digits at the start of the last segment:
     `feat/1234-description` → `1234`.
   - Never read digits out of `task-<TS>` or `pr-<n>-<TS>`: there they are a timestamp and a pull request.
   - Found → `closing_line = Closes https://github.com/goravel/goravel/issues/<number>`; otherwise omit it.

3. **Detect existing PR**
   - Run `gh pr view "$branch" --json number,title,body,headRefName`. Success → existing PR. "no pull requests found" → no PR.

4. **Commit pending work**
   - Run `git status --short`. If the working tree is dirty, stage and commit only the reviewed changes with a concise message summarizing the change.
   - If unrelated or unexpected changes are present, stop and report them instead of committing.

5. **Squash per-phase commits**
   - The implement loop commits one `phase <N>` commit per round; collapse them into a single commit before pushing.
   - Resolve `BASE_BRANCH` from the prompt; fall back to the repo's detected default branch if absent.
   - Only when every commit in `"origin/$BASE_BRANCH"..HEAD` is a `phase <N>` commit from this run (check `git log --oneline "origin/$BASE_BRANCH"..HEAD`; skip otherwise):
     ```bash
     BASE=$(git merge-base HEAD "origin/$BASE_BRANCH")
     git reset --soft "$BASE"
     git commit -m "<concise summary>"
     ```

   - This rewrites commits that may already be on the remote, so step 6 pushes with `--force-with-lease`.

6. **Push**
   - Run `git push`. If the branch has no upstream (`git rev-parse --abbrev-ref --symbolic-full-name @{u}` fails), run `git push -u origin "$branch"` — `gh pr create` requires the branch on the remote.
   - After a squash (or any rewrite of pushed commits) the remote still holds the old history: `git push --force-with-lease`, or `git push --force-with-lease -u origin "$branch"` on the first push. Never a bare `--force`.

7. **Build change context**
   - Compute base: `base=$(git merge-base HEAD "origin/$BASE_BRANCH")`.
   - Compute diff: `git diff "$base"...HEAD`.
   - Derive behavior-oriented summary bullets from the diff.
   - Detect PR type (`feature` or `bug fix`) from branch intent and diff semantics.
   - Derive code example(s) in the style of https://github.com/goravel/example per the Why Rules above.

8. **Compose PR body**
   - Fill the PR Body Specification template using the context from step 7.

9. **Validate before applying**
   - Confirm the composed body satisfies all constraints in the PR Body Specification.
   - Ensure no placeholder markers (`<...>`) remain in the final body.

10. **Determine title**
    - Summarize from the logic changes, or fall back to the branch name.
    - Title must satisfy: https://github.com/goravel/.github/blob/master/.github/workflows/check_pr_title.yml

11. **Apply**
    - Existing PR: `gh pr edit <number> --body-file <tempfile>`
    - No PR: `gh pr create --title "<title>" --body-file <tempfile> --base "$BASE_BRANCH" --head "$branch"`
    - Write the body to a temp file under `/tmp` via a bash heredoc (file edits may be denied); never write into the repo.

12. **Verify**
    - Run `gh pr view "$branch" --json number,title,url,body`.
    - Confirm required sections and constraints are satisfied.

---

## Output Contract

Return:

- Action: `created` or `updated`
- New branch name, if one was created
- PR number and URL
- Extracted issue number (or `none`)
- Final Summary bullets (2–3)
- Exact code example block(s) used in `## Why`

The caller may supply `BASE_BRANCH` in the prompt and may request the report be written to a specific path.

---

## Guardrails

- Never create duplicate PRs for the same branch.
- Never omit `## Summary` or `## Why`.
- Never reorder sections or add extra top-level headings.
- Prefer concise, deterministic output.
