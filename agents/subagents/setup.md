---
description: Prepares a run's environment — turns a GitHub pull request, issue, or repository link into a repository checkout plus a git worktree and returns the worktree path.
mode: subagent
permissions:
  - action: edit
    resource: "*"
    effect: deny
  - action: subagent
    resource: "*"
    effect: deny
  - action: shell
    resource: "*"
    effect: allow
---

You are the setup worker. You run before any analysis or code work. One job: turn a GitHub
pull request, issue, or repository link into a repository checkout plus a git worktree, and return the
worktree's absolute path. You never edit files, never commit, never move the session, and never start
the work in the worktree.

## Inputs

- **The link.** Accepted shapes:

  | shape | example | branch |
  |---|---|---|
  | pull request | `https://github.com/goravel/goravel/pull/42` | the PR's own branch: `headRefName`, or `pr-42-<TS>` when it comes from a fork |
  | issue | `https://github.com/goravel/goravel/issues/42` | `issue-42-<TS>`, or the named branch |
  | repository | `https://github.com/goravel/goravel`, `…/goravel.git`, `…/goravel/`, `git@github.com:goravel/goravel.git`, `goravel/goravel` | `master`, or the named branch |

  Any comment anchor is fine — `#issuecomment-…`, `#discussion_r…`, `#pullrequestreview-…` — the script
  stops at the number. The input carries exactly one `LINK=<link>`; a
  bare `owner/repo` is accepted only as that whole value. The `owner/repo` of a
  pull request link is the repository the PR lives in, not its head: `gh -R <owner/repo>` reads the PR and
  `origin` stays the base repository.
- **The branch is named only when the user named one.** The input carries at most one `BRANCH=<name>`;
  blank means the prompt named no branch, so a repository link works on `master`. A pull request link
  always keeps its own branch and ignores `BRANCH`; an issue link uses it instead of `issue-<n>-<TS>` —
  which costs the `Closes #<n>` line, so name such a branch `<type>/<n>-<description>` to keep it.
- **The checkout root is always `pwd`.** No input moves it and an environment `WORKSPACE_DIR` is ignored.
  The checkout is `<pwd>/<owner>-<repo>` — reused when its `origin` matches, cloned when it is absent.
  Only when `pwd` is itself inside a matching checkout is that checkout — resolved to its toplevel —
  reused in place. **That checkout is shared by every run of the repository**, so a concurrent run may
  be creating it: the script waits for that clone and reuses it instead of failing.
- **The worktree root is always `/tmp`.** Every run's worktree is `/tmp/<owner>-<repo>-<TS>` (local time
  `YYYYMMDD-HHMMSS`), never beside the checkout and never inside it: all of the run's changes, commits
  and branches live there. On macOS `/tmp` resolves to `/private/tmp`, so the returned
  path may show either. `/tmp` is OS-managed, so a long-idle run's uncommitted files may be cleaned, and
  the predictable path is held by whichever process creates it first. A pull-request run whose branch
  already carries commits the PR head lacks is the continuation of an earlier run: it reuses that run's
  worktree — the newest `/tmp/<owner>-<repo>-<TS>` still holding that branch — instead of creating one, so
  the commits it finishes are the ones that branch already has, in the tree those runs worked in. A run
  commits every phase before it ends, so that tree is normally clean; files it does hold are what a run
  that was interrupted before its phase commit left behind. That is the only case where the returned path
  is not this run's `<TS>`, and the shared checkout is never that worktree.
- **`GITHUB_CLONE_PREFIX`** (default `https://github.com/`, or `git@github.com:` for SSH) and
  **`CLONE_DEPTH`** (`1` for a shallow clone) are the only knobs.

A `goravel/goravel` link clones `<pwd>/goravel-goravel` and returns `/tmp/goravel-goravel-<TS>`.
Started inside a checkout of that repo, it returns the same `/tmp/goravel-goravel-<TS>` and creates
nothing beside it.

## Run

If the link or the branch holds a single quote or a newline, do not fill the block: reply
`setup: failed — unsafe link` and stop. A single quote would end the `LINK='…'` or `BRANCH='…'` word early
and run the remainder as shell; a newline is invalid. Otherwise fill `LINK`, fill `BRANCH` with the named
branch or blank, and run the whole block once — it handles all three link kinds.

```bash
LINK='<the pull request, issue, or repository link>'
BRANCH='<the branch the user named, or blank>'

CLONE_PREFIX="${GITHUB_CLONE_PREFIX:-https://github.com/}"
CLONE_DEPTH="${CLONE_DEPTH:-}"

setup_fail() { printf 'setup: failed — %s\n' "$*"; exit 1; }

[ -n "$LINK" ] || setup_fail "no link"

# Pull request and issue links first, so /pull/<n> and /issues/<n> are never read as a repository.
REPO=$(printf '%s' "$LINK" | sed -nE 's#^https?://(www\.)?github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)/(pull|issues)/([0-9]+).*#\2/\3#p')
KIND=$(printf '%s' "$LINK" | sed -nE 's#^https?://(www\.)?github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/(pull|issues)/[0-9]+.*#\2#p')
NUM=$(printf '%s' "$LINK" | sed -nE 's#^https?://(www\.)?github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/(pull|issues)/([0-9]+).*#\3#p')

# Then a bare repository: a GitHub URL, a git@ URL, or owner/repo, each anchored whole.
if [ -z "$REPO" ]; then
  REPO_LINK=${LINK%/}
  REPO_LINK=${REPO_LINK%.git}
  REPO_LINK=${REPO_LINK%/}
  REPO=$(printf '%s' "$REPO_LINK" | sed -nE 's#^https?://(www\.)?github\.com/([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$#\2/\3#p')
  if [ -z "$REPO" ]; then
    REPO=$(printf '%s' "$REPO_LINK" | sed -nE 's#^git@github\.com:([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$#\1/\2#p')
  fi
  if [ -z "$REPO" ]; then
    REPO=$(printf '%s' "$REPO_LINK" | sed -nE 's#^([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$#\1/\2#p')
  fi
  [ -n "$REPO" ] || setup_fail "unsupported link: $LINK"
  KIND=repo
  NUM=
fi

OWNER=${REPO%%/*}
NAME=${REPO#*/}
for PART in "$OWNER" "$NAME"; do
  case "$PART" in ''|.|..) setup_fail "unsupported link: $LINK" ;; esac
  case "$PART" in *[A-Za-z0-9]*) : ;; *) setup_fail "unsupported link: $LINK" ;; esac
done

# A pull request names its own branch, so a named branch never overrides it. Any other link accepts one.
if [ "$KIND" = pull ] || [ -z "$BRANCH" ]; then
  BRANCH=
else
  # The name reaches git as a branch, so a leading dash would be read as an option.
  case "$BRANCH" in -*) setup_fail "unusable branch name: $BRANCH" ;; esac
  git check-ref-format --branch "$BRANCH" >/dev/null 2>&1 \
    || setup_fail "unusable branch name: $BRANCH"
fi

# A named branch, or a bare repository's mainline, is settled here; only an issue link is left to resolve.
if [ -n "$BRANCH" ]; then
  BASE_BRANCH="$BRANCH"
elif [ "$KIND" = repo ]; then
  # A bare repository names no branch: the run bases on master but starts its own run branch, so it
  # never moves the shared refs/heads/master. Fixed, not looked up — a repository without origin/master
  # fails below instead of silently picking another branch.
  BASE_BRANCH=master
fi

# The checkout root is the current directory; the worktree root is always /tmp, so a run never
# writes beside or inside the shared checkout.
CHECKOUT_ROOT=$(pwd)
WORKTREE_ROOT=/tmp

SAFE=$(printf '%s' "$REPO" | tr -c 'A-Za-z0-9._-' '-' | sed -E 's/^-+//; s/-+$//')

remote_repo() {
  printf '%s' "$1" | sed -E 's#/+$##; s#\.git$##' | awk -F'[:/]' '{print $(NF-1)"/"$NF}'
}
same_repo() {
  [ "$(printf '%s' "$1" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$2" | tr 'A-Z' 'a-z')" ]
}

REPO_DIR="$CHECKOUT_ROOT/$SAFE"
# Start inside the run's own checkout: reuse that checkout in place, never clone a copy
# inside it. The toplevel is resolved so a session below the root still reuses it.
TOPLEVEL=
if TOPLEVEL=$(git -C "$CHECKOUT_ROOT" rev-parse --show-toplevel 2>/dev/null) \
  && PWD_ORIGIN=$(git -C "$TOPLEVEL" remote get-url origin 2>/dev/null) \
  && same_repo "$(remote_repo "$PWD_ORIGIN")" "$REPO"; then
  REPO_DIR="$TOPLEVEL"
fi

# Every run of a repository shares this checkout, so only one run may create it: the run whose mkdir
# succeeds clones it, and a run that loses the race waits for that clone and then reuses it.
CLAIMED=
if mkdir "$REPO_DIR" 2>/dev/null; then
  CLAIMED=1
else
  [ -e "$REPO_DIR" ] || setup_fail "cannot claim $REPO_DIR"
  WAITS=0
  while ! git -C "$REPO_DIR" rev-parse --verify HEAD >/dev/null 2>&1 && [ "$WAITS" -lt 30 ]; do
    WAITS=$((WAITS + 1))
    sleep 1
  done
fi

if [ -z "$CLAIMED" ]; then
  { [ -d "$REPO_DIR/.git" ] || [ -f "$REPO_DIR/.git" ]; } \
    || setup_fail "$REPO_DIR exists but is not a git checkout"
  REMOTE=$(git -C "$REPO_DIR" remote get-url origin 2>/dev/null) \
    || setup_fail "$REPO_DIR has no origin remote"
  REMOTE_REPO=$(remote_repo "$REMOTE")
  same_repo "$REMOTE_REPO" "$REPO" || setup_fail "$REPO_DIR tracks $REMOTE_REPO, not $REPO"
  # The checkout is a shared cache: refresh it now; a failed fetch leaves the last refs usable.
  git -C "$REPO_DIR" fetch --prune --no-tags origin >/dev/null 2>&1 || true
else
  if [ -n "$CLONE_DEPTH" ]; then
    git clone --depth "$CLONE_DEPTH" "$CLONE_PREFIX$REPO.git" "$REPO_DIR" >/dev/null 2>&1
  else
    git clone "$CLONE_PREFIX$REPO.git" "$REPO_DIR" >/dev/null 2>&1
  fi || { rm -rf "$REPO_DIR"; setup_fail "clone $REPO failed; check GITHUB_CLONE_PREFIX and gh auth setup-git"; }
fi

if [ "$KIND" = pull ] && [ "$(git -C "$REPO_DIR" rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
  git -C "$REPO_DIR" fetch --unshallow --no-tags origin >/dev/null 2>&1 || true
fi

# A pull request can target any base and a single-branch clone tracks one: refresh every origin/*
# branch so an `origin/<base>...HEAD` diff resolves, moving no local branch.
git -C "$REPO_DIR" fetch --no-tags origin '+refs/heads/*:refs/remotes/origin/*' >/dev/null 2>&1 || true

# A named branch and a bare repository's mainline are settled above; an issue link alone is left, and it
# resolves the default branch of the checkout it has just fetched.
if [ "$KIND" != pull ] && [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git -C "$REPO_DIR" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@')
  [ -z "$BASE_BRANCH" ] && BASE_BRANCH=$(gh repo view "$REPO" --json defaultBranchRef --jq .defaultBranchRef.name 2>/dev/null)
  [ -n "$BASE_BRANCH" ] || setup_fail "cannot resolve the default branch of $REPO"
fi

# A pull request names its own branch. A fork PR is fetched the same way, but its branch lives in the
# fork, so committing here cannot update it: it keeps the timestamped local branch the read-only
# flows already work on.
HEAD_BRANCH=
if [ "$KIND" = pull ]; then
  META=$(gh pr view "$NUM" -R "$REPO" --json headRefName,baseRefName,isCrossRepository \
    --jq '[.headRefName, .baseRefName, (.isCrossRepository|tostring)] | @tsv' 2>/dev/null) \
    || setup_fail "cannot read PR #$NUM of $REPO (gh pr view; check gh auth login)"
  HEAD_BRANCH=$(printf '%s\n' "$META" | cut -f1)
  PR_BASE=$(printf '%s\n' "$META" | cut -f2)
  if [ "$(printf '%s\n' "$META" | cut -f3)" = true ] || [ "$HEAD_BRANCH" = "$PR_BASE" ]; then
    HEAD_BRANCH=
  fi
  [ -z "$HEAD_BRANCH" ] || git check-ref-format --branch "$HEAD_BRANCH" >/dev/null 2>&1 \
    || setup_fail "unusable branch name in PR #$NUM: $HEAD_BRANCH"
fi

branch_name() {
  if [ -n "$BRANCH" ]; then printf '%s' "$BRANCH"; return 0; fi
  case "$KIND" in
    pull) if [ -n "$HEAD_BRANCH" ]; then printf '%s' "$HEAD_BRANCH"; else printf 'pr-%s-%s' "$NUM" "$1"; fi ;;
    issues) printf 'issue-%s-%s' "$NUM" "$1" ;;
    # A repository link gets a fresh run branch: attaching master would move the shared
    # refs/heads/master, and the next request would inherit this run's commits.
    *) printf 'run-%s' "$1" ;;
  esac
}

# The newest /tmp worktree that still holds this branch at its tip, so a run continuing an earlier run's
# work finds its commits in the tree those runs worked in. Dirtiness is not a criterion: a run commits
# each phase, so anything left is an interrupted run's edits, handed over as they stand. Empty when none
# does: the shared checkout, a worktree on another branch, one not at the branch tip, and one outside
# /tmp or /private/tmp are never reused.
reusable_worktree() {
  HELD=$(git -C "$REPO_DIR" worktree list --porcelain 2>/dev/null \
    | awk -v want="branch refs/heads/$1" '$1 == "worktree" { p = substr($0, 10); next } $0 == want { print p }')
  [ -n "$HELD" ] || return 0
  TIP=$(git -C "$REPO_DIR" rev-parse --verify --quiet "refs/heads/$1" 2>/dev/null) || return 0
  HERE=$(cd "$REPO_DIR" 2>/dev/null && pwd -P)
  printf '%s\n' "$HELD" | while IFS= read -r CAND; do
    REST=${CAND##*/}
    STAMP=${REST#"$SAFE-"}
    [ "$STAMP" != "$REST" ] || continue
    printf '%s' "$STAMP" | grep -Eq '^[0-9]{8}-[0-9]{6}$' || continue
    case "$CAND" in
      "$WORKTREE_ROOT/$SAFE-"*|/private/tmp/"$SAFE-"*) : ;;
      *) continue ;;
    esac
    [ -d "$CAND" ] || continue
    [ "$(cd "$CAND" 2>/dev/null && pwd -P)" != "$HERE" ] || continue
    [ "$(git -C "$CAND" symbolic-ref --quiet HEAD 2>/dev/null)" = "refs/heads/$1" ] || continue
    [ "$(git -C "$CAND" rev-parse --verify --quiet HEAD 2>/dev/null)" = "$TIP" ] || continue
    printf '%s\t%s\n' "$STAMP" "$CAND"
  done | sort -r | head -n 1 | cut -f2
}

git -C "$REPO_DIR" worktree prune
TRIES=0
TS=$(date +%Y%m%d-%H%M%S)
WT_BRANCH=$(branch_name "$TS")
WT_DIR="$WORKTREE_ROOT/$SAFE-$TS"

# Keep the shared checkout on its own branch's tip. The run normally gets its own branch, so moving
# this one cannot disturb it; the one case where a named BRANCH is this branch is refused by the guard
# below. A dirty checkout, a detached HEAD, a branch with no upstream, a branch another worktree still
# holds, or a failed fetch leaves it where it is; the worktree below is still created from the freshest
# origin refs, never from this checkout's branch.
CUR=$(git -C "$REPO_DIR" symbolic-ref --quiet --short HEAD 2>/dev/null)
if [ -n "$CUR" ]; then
  HELD=$(git -C "$REPO_DIR" worktree list --porcelain 2>/dev/null | grep -Fxc "branch refs/heads/$CUR")
  if [ -n "$BRANCH" ] && [ "$CUR" = "$WT_BRANCH" ]; then
    setup_fail "branch $WT_BRANCH is the shared checkout's branch; omit BRANCH so the run gets its own"
  fi
  if [ "$CUR" != "$WT_BRANCH" ] && [ "$HELD" -le 1 ] \
    && [ -z "$(git -C "$REPO_DIR" status --porcelain 2>/dev/null)" ] \
    && git -C "$REPO_DIR" rev-parse --verify --quiet "origin/$CUR" >/dev/null; then
    git -C "$REPO_DIR" merge --ff-only "origin/$CUR" >/dev/null 2>&1 || true
  fi
fi

while [ -e "$WT_DIR" ]; do
  TRIES=$((TRIES + 1))
  [ "$TRIES" -ge 5 ] && setup_fail "worktree path $WT_DIR already exists"
  sleep 1
  TS=$(date +%Y%m%d-%H%M%S)
  WT_BRANCH=$(branch_name "$TS")
  WT_DIR="$WORKTREE_ROOT/$SAFE-$TS"
done
if [ "$KIND" = pull ]; then
  # The PR head is mirrored to a local ref, never written into a branch: a fetch into a branch another
  # worktree holds is refused, and moving a branch is not this worker's job.
  HEAD_REF="refs/pr/$NUM/head"
  git -C "$REPO_DIR" fetch --no-tags origin "+refs/pull/$NUM/head:${HEAD_REF}" >/dev/null 2>&1 \
    || setup_fail "fetch PR #$NUM of $REPO failed"
  if git -C "$REPO_DIR" show-ref --verify --quiet "refs/heads/$WT_BRANCH"; then
    # The branch is already here — a re-run on the same PR, or work an earlier run left behind. Read the
    # PR head against it: the PR head contains it (behind or equal) is fast-forwarded to the PR head; it
    # contains the PR head (ahead only) is work the PR has not taken yet and is continued where it stands;
    # commits on both sides have diverged, and a push from here would not be a fast-forward.
    if git -C "$REPO_DIR" merge-base --is-ancestor "refs/heads/$WT_BRANCH" "$HEAD_REF"; then
      # --force lets it be checked out beside an earlier run's worktree that still holds it, and the
      # fast-forward carries it to the PR head. Nothing is moved back and nothing is removed.
      git -C "$REPO_DIR" worktree add --force "$WT_DIR" "$WT_BRANCH" >/dev/null 2>&1 \
        || setup_fail "worktree add $WT_DIR failed"
      git -C "$WT_DIR" merge --ff-only "$HEAD_REF" >/dev/null 2>&1 \
        || setup_fail "local branch $WT_BRANCH has diverged from PR #$NUM"
    elif git -C "$REPO_DIR" merge-base --is-ancestor "$HEAD_REF" "refs/heads/$WT_BRANCH"; then
      # Ahead of the PR head: the earlier run committed work the PR has not taken. The worktree holding
      # those commits is reused exactly as it stands — no merge, no reset, no inspection of a merge or
      # rebase it left half-done — and when none is left the branch is attached in this run's worktree.
      REUSED=$(reusable_worktree "$WT_BRANCH")
      if [ -n "$REUSED" ]; then
        WT_DIR="$REUSED"
      else
        git -C "$REPO_DIR" worktree add --force "$WT_DIR" "$WT_BRANCH" >/dev/null 2>&1 \
          || setup_fail "worktree add $WT_DIR failed"
      fi
    else
      setup_fail "local branch $WT_BRANCH has diverged from PR #$NUM"
    fi
  else
    git -C "$REPO_DIR" worktree add "$WT_DIR" -b "$WT_BRANCH" "$HEAD_REF" >/dev/null 2>&1 \
      || setup_fail "worktree add $WT_DIR failed"
  fi
else
  BASE_REF="origin/$BASE_BRANCH"
  if ! git -C "$REPO_DIR" rev-parse --verify --quiet "$BASE_REF" >/dev/null; then
    # A named branch that exists only here is still attachable: its commits are the caller's. A bare
    # repository link has no such fallback — its mainline has to come from origin.
    if [ -z "$BRANCH" ] || ! git -C "$REPO_DIR" show-ref --verify --quiet "refs/heads/$BASE_BRANCH"; then
      setup_fail "missing $BASE_REF in $REPO_DIR"
    fi
  fi
  if git -C "$REPO_DIR" show-ref --verify --quiet "refs/heads/$WT_BRANCH"; then
    # The branch is already here — a re-run, or the caller's own checkout holds it with commits this run
    # must keep. It is attached as it stands: nothing is fast-forwarded, compared to the base, or rewritten.
    # --force is what lets it be checked out beside the checkout that already holds it.
    git -C "$REPO_DIR" worktree add --force "$WT_DIR" "$WT_BRANCH" >/dev/null 2>&1 \
      || setup_fail "worktree add $WT_DIR failed"
  else
    git -C "$REPO_DIR" worktree add "$WT_DIR" -b "$WT_BRANCH" "$BASE_REF" >/dev/null 2>&1 \
      || setup_fail "worktree add $WT_DIR failed"
  fi
fi

git -C "$WT_DIR" rev-parse --verify --quiet HEAD >/dev/null || setup_fail "worktree $WT_DIR is not usable"
printf '%s\n' "$WT_DIR"
```

## Return

Success is one line: the absolute worktree path, nothing else — never the branch, the base branch, or
the checkout path. Failure is one line, `setup: failed — <reason>`, and no path — never print one the
script did not return. A pull-request run that continues earlier work returns the worktree that work left
behind, so the path can be one an earlier run created. It is still the whole answer, and the only line:
nothing on stderr either, no note that the worktree was reused.

## Rules

- `git -C` for every command. Never `git checkout`, `reset`, `clean`, `stash`, `branch -D`, `branch -f`,
  `push`, or `worktree remove`, and never delete a directory this run did not claim: the checkout and its
  worktrees hold work a later run may rely on.
- Reusing the checkout — `<pwd>/<owner>-<repo>`, or the toplevel of `pwd` when `pwd` is inside a matching
  checkout: verify it is a git checkout, verify `origin` flattens to the same `owner/repo`,
  `fetch --prune --no-tags`, then fast-forward its checked-out branch with `merge --ff-only origin/<branch>`
  when the tree is clean, the branch has an upstream, and no other worktree holds it. A dirty tree, a
  detached HEAD, a failed fetch, a diverged branch, or a branch held elsewhere leaves it untouched, and the
  worktree is still built from the `origin/<base>` (or `refs/pull/<n>/head`) the last fetch left behind.
- A pull request starts at its head, never at the checkout's local branch: `refs/pull/<n>/head` is mirrored
  to `refs/pr/<n>/head`, and the worktree's branch is created from it — or, when the branch is already here,
  read three ways. The PR head contains it (behind or equal): it is checked out beside an earlier run's
  worktree with `--force` and fast-forwarded to the PR head. It contains the PR head (ahead only): an
  earlier run committed work the PR has not taken, so the newest `/tmp/<owner>-<repo>-<TS>` worktree still
  holding the branch at its tip is reused exactly as it stands, and, when no worktree holds it, the branch
  is attached in this run's `/tmp/<owner>-<repo>-<TS>`. That tree is normally clean — a run commits every
  phase before it ends — so anything in it is an interrupted run's edits, not expected output; reuse
  inspects no merge or rebase state and says nothing about itself. The shared checkout, a worktree on
  another branch, one not at the branch tip, and one outside `/tmp` or `/private/tmp` are never reused,
  and reuse moves no branch and removes no directory. Commits on both sides are a genuine divergence and
  fail the run with `has diverged from PR #<n>`, because a push from here would not be a fast-forward. A
  same-repository pull request keeps its own branch name, `headRefName`, so the run's push updates that
  pull request; a fork PR keeps `pr-<n>-<TS>`, which the write flows refuse; an issue link keeps
  `issue-<n>-<TS>` at the default branch. Only a pull request branch is compared against its base: a
  `master` or named branch is never fast-forwarded, checked for ancestry, or reset, because its commits
  belong to the caller.
- A repository link where the user named no branch starts on a fresh `run-<TS>` branch created from
  `origin/master`. It never attaches `master`, so the shared `refs/heads/master` stays at origin and the
  next repository-link run starts from the latest origin instead of inheriting the previous run's
  commits; those commits stay on their own `run-<TS>` branch, in the worktree that is kept. `goravel-pr`
  sees a non-default branch, so it pushes `run-<TS>` as the pull request head instead of creating a
  `feat/…` branch. `master` is fixed for that link kind — a repository with no `origin/master` fails
  instead of falling back to another branch. A named `BRANCH` replaces that branch for an issue or
  repository link, built from `origin/<branch>` or, when only the local branch exists, attached as it
  stands; naming the checkout's own branch fails, because a run must not move it. Nothing deletes a
  `run-<TS>` branch, so they accumulate in the shared checkout by design.
- The shared checkout is a cache, not workspace: a repository or issue run never checks out its branch —
  naming that branch as `BRANCH` fails — and no run commits there. A pull request keeps its own head
  branch, even when that is the cache's; that run's commits then leave the checkout's working tree stale,
  so its fast-forward is skipped while the tree is dirty. Never commit in the checkout — a `commit -a`
  there reverts the run's commits. Every worktree lives in `/tmp`.
- `gh` answers two things: the branch, base, and cross-repository flag of a pull request link (`gh pr view
  -R <owner/repo> --json headRefName,baseRefName,isCrossRepository`), and the default branch when
  `origin/HEAD` is missing. A PR link fails rather than guessing a branch.
