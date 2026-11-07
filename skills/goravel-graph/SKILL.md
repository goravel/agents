---
name: Goravel Graph
description: >
  Code-graph procedure shared by the Goravel agents. Use this skill whenever an
  agent needs to build or query the code graph — it owns the build call, the query
  tools, the once-per-run build rule, the empty-graph fallback, and how the graph
  may appear in output.
---

# Goravel Graph

This skill is the only place the code-graph calls are written down. Agents load it and follow it; no
other skill or agent repeats the tool calls.

## Build

Build once per run or round, before the work that reads it — never per finding, never per file:

```
build_or_update_graph_tool({ repo_root: "<the run's worktree>", base: "<the diff base>" })
```

- `repo_root` is the worktree the caller works in, never the parent checkout beside it.
- `base` is the caller's diff base: `$DIFF_BASE` in `agents/subagents/review-diff.md`, `$GRAPH_BASE` in
  `agents/review.md` (the last reviewed head, else `origin/<BASE_BRANCH>`), and `origin/<BASE_BRANCH>` in
  `agents/analyze.md`. A caller with no diff base omits `base` and builds on `repo_root` alone — the
  plan agent is that case.
- One build per run, one build per review round. A new round means a new base, so build again at the start
  of the round — not once per worker, not once per finding.
- The agent that builds is the agent that fans out. A worker it spawns reads the graph the caller built and
  never builds again.

## Query

Six calls cover every read:

```
get_minimal_context_tool({ task, repo_root, changed_files, base })
detect_changes_tool({ detail_level: "minimal", repo_root, changed_files, base })
query_graph_tool({ repo_root, pattern: "callers_of", target })
query_graph_tool({ repo_root, pattern: "tests_for", target })
semantic_search_nodes_tool({ repo_root, query: "<key term>" })
get_review_context_tool({ detail_level: "minimal", repo_root, include_source: false, base })
```

- Prefer the graph over grep for finding a definition, a caller, a test, or the shape of a changed file.
- The graph is a map, not a source of truth: open every caller, callee, or test in the worktree before you
  count it or report it. A count nobody opened is a guess.
- `callers_of` answers "who calls this". It does not list interface implementors — find those with
  `rg -n "func \([^)]*\) <Method>\("`.
- A changed file is context, not evidence. `detect_changes_tool` and `get_review_context_tool` point at the
  lines; read the file for what they say.

## When the graph is not available

Never wait, retry, or rebuild to fix one of these — read the files instead:

- `status: not_ready` → carry on with the work; read the diff and the source.
- An empty graph, or a language the parser does not cover → grep, glob, and read. When a report shows a
  count that came from search, say so in that line.
- A docs-only repository — prompts, skills, Markdown — always returns an empty graph. Read the files. Do not
  rebuild, and do not treat the empty result as a failure.

## In output

Never paste a graph tool call, a tool name, or a graph status into a review, a report, or a pull-request
body. The reader sees findings and facts, not how they were found. A caller count appears as a number, with
the file the reader can open beside it.
