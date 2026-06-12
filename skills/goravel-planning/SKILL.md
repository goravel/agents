---
name: goravel-planning
description: >
  Planning workflow for Goravel tasks. Use this skill whenever the user asks
  for a plan, investigation, or implementation outline and the response should
  show both the concrete code change and the real user-facing code that the
  change enables.
---

# Goravel Planning

Produces a complete, reviewable implementation plan before any code is written. The goal is to surface every file that needs changing, every invariant to preserve, every ambiguity that needs a decision — so that implementation can proceed without surprises.

This skill is read-only. It explores the codebase, reads relevant files, and writes a plan. It does not edit or create source files.

## Workflow

Follow these steps in order. Do not start writing the plan until Steps 1–4 are complete.

### Step 1: Understand the Task

Extract from the user's message:

- **What** is being added or changed (new feature, new endpoint, refactor, bug fix, etc.)
- **Business rules** — constraints, invariants, edge cases, exclusions
- **Scope boundaries** — what components / modules / contexts this applies to (and what it explicitly does NOT apply to)
- **Ambiguities** — anything the description leaves unclear

### Step 2: Locate the Affected Code

Determine which service, module, or package is the primary target. Use targeted searches to understand the project layout before reading files:

```bash
# Discover project structure
find . -type f | head -40
# Locate relevant files by pattern or keyword
grep -r "<key term>" --include="*.<ext>" -l | head -20
```

Identify the root of the affected component and understand its layout (source files, tests, config, schema, migrations, etc.).

### Step 3: Read Existing Patterns

Read the most relevant existing files to understand:

- **Schema / models** — data structures, naming conventions, existing values
- **Core logic** — existing predicates, helpers, dispatch patterns
- **Validation / guards** — how input is validated, what constraints are enforced
- **Configuration / build** — how the project is built, what tooling runs after changes
- **Tests** — how existing test suites are structured, what patterns look like

Read enough to know the exact code you will reference in the plan, but do not read entire files if a targeted grep suffices.

### Step 4: Identify All Touch Points

Build a complete list of every file that needs to change, organized by layer. For each file, note what specifically changes and why. Common layers to consider:

| Layer | Examples | What changes |
|---|---|---|
| Schema / models | `.thrift`, `.proto`, `.sql`, `.graphql`, type files | New fields, values, types |
| Generated code | Auto-generated stubs, clients | Rebuilt via project tooling |
| DB migrations | Migration files | Schema changes |
| Core logic | Service, handler, util files | New functions, updated dispatch |
| Validation | Validator, guard files | New rules, updated checks |
| Tests | `*_test.*` files | New test cases covering the change |
| Config / build | Build files, manifests | New dependencies, targets |

Adapt this to the actual project structure — discover the equivalent layers by reading the codebase.

### Step 5: Write the Plan

Output the plan in the format below. Be specific — include exact file paths, exact new identifiers, exact code snippets for non-trivial additions. The reader should be able to implement from the plan without opening the source files themselves.

---

## Plan Output Format

```
## Summary

<1–3 sentence description of what is being added and the key business rules it must satisfy.>

Rules:
- <rule 1>
- <rule 2>
- ...

---

## Files to Change

### 1. <Layer name> — <one-line description>

**File:** `<exact path>`

<Explanation of what changes and why. Include the exact new code or pseudocode for non-trivial additions. For schema changes, include the full before/after. For logic changes, include the exact function signatures and key logic.>

### 2. <Layer name> — ...

...

---

## Sequence

Ordered implementation steps with dependencies called out explicitly:

1. <step> → <what it unblocks>
2. <step>
...
N. Run tests to verify

---

## Real User Code Example

the end-user application code that would use or observe the change. Do not stop at abstract steps like "update middleware" or "add validation".
Show the shape of the real code.

<Code snippet showing how a Goravel user would interact with the new feature or change>

## Open Questions

Things that must be confirmed before implementation:

1. <question> — <why it matters, what the two possible answers imply>
2. ...

(Omit this section if there are no genuine ambiguities.)
```

---

## What counts as actual code

Use `Actual code` to show the likely implementation shape in this repository,
such as:

- contracts added under `contracts/`
- framework wiring in `foundation/` or `service_provider.go`
- facade or module behavior in package code
- tests that prove the framework behavior

Example:

```go
type Middleware interface {
	Handle(ctx http.Context) error
	Name() string
}
```

## What counts as real user code

Use `Real user code` to show code a Goravel application author would actually
write in a real app, not internal framework implementation.

Prefer patterns from `goravel/goravel` and `goravel/example`, such as:

- route registration
- middleware usage
- controller code
- request validation
- ORM queries
- auth flows

Example:

```go
facades.Route().Middleware(middleware.Jwt()).Get("users", userController.Index)
```

## Quality Bar for the Plan

Before outputting, verify:

- Every file that needs changing is listed — no layer silently left out
- File paths are exact (verified by the searches in Steps 2–3)
- Code snippets use the correct identifiers (function names, type names, constant names) as they actually exist in the codebase, not guesses
- The sequence respects actual build and dependency order for this project
- Open Questions are genuine ambiguities, not things that can be determined by reading the code
- The plan does not contain implementation — only description of what to implement
- If the work affects user-facing behavior, include `Real User Code Example`. 
- If the work is entirely internal, say that explicitly and omit the user code example only when no realistic user-facing snippet exists.
- Never use pseudo-code placeholders inside code fences. Use realistic Goravel code patterns.
