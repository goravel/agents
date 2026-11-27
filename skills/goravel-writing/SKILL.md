---
name: Goravel Writing
description: >
  Plain-language rules for text a person reads — pull-request bodies, review findings, replies to review
  comments, and analysis answers on GitHub, plus the plan and the run summary in chat. Load before
  composing, emitting, or posting any of them. Not for code comments; those follow agents/subagents/code.md.
---

# Plain Language

One rule set for every line a person reads — text posted on GitHub and text printed in chat. A skill or
agent that writes reader-facing text loads this skill. It keeps only the rules for its own artifact: its
sections, its anchors, its counts. Those never loosen the rules below.

## The lead

The first sentence of a block is the answer: what changed, what the reader must do, or the verdict.
A reader who stops after one sentence still knows the point.

## Sentences

- 20 words maximum per sentence. Split it; do not join two clauses with "and".
- One fact per sentence, one fact per line, three lines maximum per paragraph.
- Bullets before prose. One bullet per fact.
- Present tense, active voice. The code is the subject: "`Paginate` returns one page."
- Name the thing in backticks: the function, the field, the file, the command. Never "the code",
  "the logic", "the implementation", "the change" when a name exists.
- Numbers, not adverbs: "2 callers", "3 files" — not "several", "numerous", "a few".
- `may` / `might` only when the result depends on input the reader controls; otherwise say what happens.

## Words

Drop the left column and write the right one:

| drop | write |
| --- | --- |
| utilize, leverage | use |
| in order to | to |
| prior to | before |
| regarding, concerning | about |
| ensure | check, make sure |
| facilitate | help |
| aforementioned, hereby, thus, hence | (delete) |
| seamless, robust, comprehensive, various, numerous | (delete, or name the thing) |
| basically, simply, just, actually, really | (delete) |
| perhaps, possibly, it seems, I think | (say it, or delete) |
| consider whether | ask the question |

No praise, no thanks, no apology, no self-narration ("I've gone ahead and", "This PR aims to"), no
closing line. Start with the fact.

## Check before emitting

1. The longest sentence is 20 words or fewer.
2. Every word in the drop column is gone.
3. The first sentence carries the point. If the text still works without it, the point is elsewhere.
4. Delete every line the reader does not have to act on. What is left is the text.

## Replying to a review comment

A reply is one or two sentences. It answers the comment and nothing else. Start with the fact — no
`Fixed:` or `No change:` label, no greeting.

- **Solved** — what changed, first: "`Paginate` now returns the row count under `meta`." Add `file:line`
  only when the reader must open it.
- **Declined** — the code fact that makes the change unnecessary, first; the last sentence says the code
  stays as it is: "`Store` already accepts a nil model, so the extra check would hide a real error. I left
  the code as is." State the code fact; never "we decided", "out of scope", or "not planned".
- **Question** — the answer first, then the effect on the code: "Yes. `Paginate` counts rows after the
  filters run, so no change is needed."

A comment that is only a question, or is wrong, is declined. The answer is the reply, and the thread stays
open.

Never promise future work ("I'll address this later"). Either it is fixed, or it is declined with the
reason.
