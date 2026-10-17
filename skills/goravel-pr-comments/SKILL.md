---
name: Goravel PR Comments
description: >
  Close out PR review comments from a caller-supplied list. For each comment it removes the
  👀 reaction, posts the reason as a reply, and resolves the thread for "solved" comments
  ("declined" ones are left open). Executor only — it never decides or triages.
---

# Close Out PR Comments

The caller supplies the list of comments. For each comment, in order: remove the 👀 reaction,
post the reason as a reply, then resolve the thread when the status is `solved`. You never add
reactions and never edit code. Each comment has a reference (review-comment URL or numeric id),
a `status` (`solved` or `declined`), and a `reason` posted verbatim.

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
1. **Remove the 👀 reaction** — every `eyes` reaction, whoever added it:
   `gh api --paginate "repos/$OWNER/$REPO/pulls/comments/$COMMENT_ID/reactions" --jq '.[] | select(.content=="eyes") | .id'`
   For each id -> `gh api -X DELETE "repos/$OWNER/$REPO/pulls/comments/$COMMENT_ID/reactions/$REACTION_ID"`.
   None -> `no 👀` and continue. A rejected delete -> `👀 delete failed` and continue.
2. **Post the reply** (reason via a `/tmp` file so quotes/newlines are safe):
   `gh api "repos/$OWNER/$REPO/pulls/$PR/comments/$COMMENT_ID/replies" -f body="$(cat "$tmp")"`
   Fails -> `reply failed`, do NOT resolve.
3. **Resolve only when `status: solved`** and the comment id has a thread in the map. No matching thread -> record `skipped (no thread)`; otherwise, when not already resolved:
   `gh api graphql -f query='mutation($threadId:ID!){ resolveReviewThread(input:{threadId:$threadId}){ thread{ isResolved } } }' -F threadId="$THREAD_ID"`
   `declined` -> stop after the reply.
4. Record `solved + resolved` / `declined (open)` / `already resolved` / `skipped (no thread)`.

## Verify
Re-run the thread-map query — every `solved` target's thread is `isResolved == true`; re-list reactions — no `eyes` remains on any target.

## Guardrails
- Only touch comments the caller listed. Never react to, reply to, or resolve an unlisted comment.
- Never resolve a `declined` comment or one whose reply failed.
- Remove every 👀 reaction regardless of owner; if none exists, continue without error.
- Top-level issue comments have no thread — reply with `gh pr comment "$PR" --body-file "$tmp"` and report `replied (not resolvable)`.
- Never edit code; never fabricate a reason — post the caller's text verbatim.

## Output Contract
PR number and URL, then one line per comment: reference -> status -> outcome (`solved + resolved` / `declined (open)` / `already resolved` / `no 👀` / `👀 delete failed` / `skipped (unparseable)` / `skipped (no thread)` / `reply failed`), plus the reason posted.
