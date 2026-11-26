---
name: Goravel PR Comments
description: >
  Close out PR review comments from a caller-supplied list. For each comment it posts the
  reason as a reply and resolves the thread for "solved" comments ("declined" ones are left
  open). A review body and a top-level issue comment get a pull-request reply and resolve
  nothing. Executor only — it never decides or triages, and never touches reactions.
---

# Close Out PR Comments

The caller supplies the list of comments. For each comment, in order: post the reason as a reply,
then resolve the thread when the status is `solved`. You never touch reactions and never edit code.
Each comment has a reference (review-comment URL, review-body URL, issue-comment URL, or numeric id),
a `status` (`solved` or `declined`), and a `reason` posted verbatim. The list is flat: a review body is
one entry and each of its inline comments is another.

## Setup
1. Resolve the PR: the `PR` value from the caller, else `gh pr view --json number --jq '.number'` for the current branch. Empty -> stop and report failure.
2. `OWNER/REPO` = `gh repo view --json nameWithOwner --jq .nameWithOwner`.
3. Resolve each comment reference to a numeric id and a kind — `inline`, `review-body`, or `issue`: an inline review comment from the digits after `#discussion_r` (a reply resolves through the thread it sits in) or from a bare number; a review body from the digits after `#pullrequestreview-` (no thread — see Guardrails); a top-level issue comment from the digits after `#issuecomment-` (no thread — see Guardrails). Unresolvable -> record `skipped (unparseable)`.
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
1. **Post the reply** — write the reason to `/tmp/pr-comment-reply.md` with a bash heredoc first (file edits are denied), so quotes and newlines are safe. Only an inline review comment replies in a thread; a review body and an issue comment reply on the pull request:

   ```bash
   if [ "$KIND" = inline ]; then
     gh api "repos/$OWNER/$REPO/pulls/$PR/comments/$COMMENT_ID/replies" -f body="$(cat /tmp/pr-comment-reply.md)"
   else
     gh pr comment "$PR" --body-file /tmp/pr-comment-reply.md
   fi
   ```

   Fails -> `reply failed`, do NOT resolve.
2. **Resolve only when `status: solved`** and the comment is an inline review comment with a thread in the map. A review body and an issue comment have no thread, so their reply is their outcome: record `replied (not resolvable)` and resolve nothing. No matching review thread -> record `skipped (no thread)`; otherwise, when not already resolved:
   `gh api graphql -f query='mutation($threadId:ID!){ resolveReviewThread(input:{threadId:$threadId}){ thread{ isResolved } } }' -F threadId="$THREAD_ID"`
   `declined` -> stop after the reply.
3. Record `solved + resolved` / `declined (open)` / `already resolved` / `replied (not resolvable)` / `skipped (no thread)`.

## Verify
Re-run the thread-map query — every `solved` inline comment's thread is `isResolved == true`; a review body and an issue comment have no thread to check.

## Guardrails
- Only touch comments the caller listed. Never reply to or resolve an unlisted comment.
- Never resolve a `declined` comment or one whose reply failed.
- Never touch reactions. The 👀 on the mention is swapped outside this skill, not here.
- Top-level issue comments and review bodies have no thread — reply with `gh pr comment "$PR" --body-file /tmp/pr-comment-reply.md` and report `replied (not resolvable)`.
- A review body is one entry, not the review: its inline comments are listed on their own lines by the caller. Never expand a review link into comments here, and never resolve one.
- Never edit code; never fabricate a reason — post the caller's text verbatim.

## Output Contract
PR number and URL, then one line per comment: reference -> status -> outcome (`solved + resolved` / `declined (open)` / `already resolved` / `replied (not resolvable)` / `skipped (unparseable)` / `skipped (no thread)` / `reply failed`), plus the reason posted.
