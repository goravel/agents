---
name: Goravel PR Comments
description: >
  Close out PR review comments from a caller-supplied list. For each comment it posts the
  reason as a reply and resolves the thread for "solved" comments ("declined" ones are left
  open). Executor only — it never decides or triages, and never touches reactions.
---

# Close Out PR Comments

The caller supplies the list of comments. For each comment, in order: post the reason as a reply,
then resolve the thread when the status is `solved`. You never touch reactions and never edit code.
Each comment has a reference (review-comment URL or numeric id), a `status` (`solved` or `declined`),
and a `reason` posted verbatim.

## Setup
1. Resolve the PR: the `PR` value from the caller, else `gh pr view --json number --jq '.number'` for the current branch. Empty -> stop and report failure.
2. `OWNER/REPO` = `gh repo view --json nameWithOwner --jq .nameWithOwner`.
3. Resolve each comment reference to a numeric review-comment id: from a URL take the digits after `#discussion_r`; a bare number is used directly. Unresolvable -> record `skipped (unparseable)`.
4. Build the thread map once (unresolved review threads -> their comment ids):

    gh api graphql -F owner="$OWNER" -F name="$REPO" -F number="$PR" -f query='
      query($owner:String!,$name:String!,$number:Int!){
        repository(owner:$owner,name:$name){
          pullRequest(number:$number){
            reviewThreads(first:100){
              nodes{ id isResolved comments(first:100){ nodes{ databaseId } } }
            }
          }
        }
      }'

   Map each `databaseId` -> `{ threadId, isResolved }`.

## Per comment
1. **Post the reply** (reason via a `/tmp` file so quotes/newlines are safe):
   `gh api "repos/$OWNER/$REPO/pulls/$PR/comments/$COMMENT_ID/replies" -f body="$(cat "$tmp")"`
   Fails -> `reply failed`, do NOT resolve.
2. **Resolve only when `status: solved`** and the comment id has a thread in the map. No matching thread -> record `skipped (no thread)`; otherwise, when not already resolved:
   `gh api graphql -f query='mutation($threadId:ID!){ resolveReviewThread(input:{threadId:$threadId}){ thread{ isResolved } } }' -F threadId="$THREAD_ID"`
   `declined` -> stop after the reply.
3. Record `solved + resolved` / `declined (open)` / `already resolved` / `skipped (no thread)`.

## Verify
Re-run the thread-map query — every `solved` target's thread is `isResolved == true`.

## Guardrails
- Only touch comments the caller listed. Never reply to or resolve an unlisted comment.
- Never resolve a `declined` comment or one whose reply failed.
- Never touch reactions. The 👀 on the mention is swapped by `subagents/reaction`, not here.
- Top-level issue comments have no thread — reply with `gh pr comment "$PR" --body-file "$tmp"` and report `replied (not resolvable)`.
- Never edit code; never fabricate a reason — post the caller's text verbatim.

## Output Contract
PR number and URL, then one line per comment: reference -> status -> outcome (`solved + resolved` / `declined (open)` / `already resolved` / `skipped (unparseable)` / `skipped (no thread)` / `reply failed`), plus the reason posted.
