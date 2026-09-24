<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral ui` runs the review loop: design

**Status: designed, not built (2026-09-24).** Nothing described here exists
yet. In particular `corral ui --write` and `corral review recheck` are
proposed, not shipped. Written the day after a full review round was
carried through by hand: an outside review, a `corral review` round, three
fix PRs, ten adjudications and two pushes to `corral/ledger`, each step
typed in a terminal.

## Intent

**What the founder asked for:** the UI should handle the review process, all
of it: start a round, watch it, adjudicate what it found, publish the record.

**What that is for:** the loop is corral's credibility. It is how the
ledger comes to hold findings, reproductions and a person's verdict on
each. Today it takes a dozen commands in the right order, and yesterday
showed the cost of that: two signed reviews sat uncommitted for nine days,
and an append would have chained new entries onto them without anyone
noticing.

**Success:** a person can run a round, rule on its findings and publish the
result from one browser tab. They see every command that ran and its output,
and nothing is weaker than the CLI: the same checks, the same refusals, the
same record.

**Assumptions (not stated by the founder):** one operator, on their own
machine; the ledger is a git checkout of `corral/ledger`; the seats are the
ones `corral review` already knows.

## Approach: the UI drives the CLI

Every write the UI makes is an existing (or new) `corral` subcommand run as
a subprocess of the same binary, plus `git` in the ledger checkout. The UI
implements no ledger logic of its own.

This is the whole point of the choice. The dominant defect in this
repository, found at a new door in round after round, is a rule held at one
door and not at another. A UI that re-implemented adjudication or publishing
would be a new door with its own copy of each rule. Driving the CLI keeps
one implementation, reached from two places.

Rejected: calling the Go packages directly (a second copy of every write
path), and reviving the brain's cockpit (`internal/ui`), which the founder
froze and which would pull the coordination platform back into the audit
path.

The front end stays the single vanilla `cmd/corral/uiweb/index.html`.

## 1. Write authority

`corral ui` stays read-only by default, exactly as today. Writing is opt-in:
`corral ui --write`.

**Launch token.** `--write` generates a 256-bit random token, held in memory
only (never on disk, gone when the process exits). It prints
`http://127.0.0.1:8787/#t=<token>` and opens the browser (`--no-open`
suppresses that). The token is in the `#fragment` because browsers never send
the fragment to the server, so it cannot reach an access log or a `Referer`
header. The page moves it into the tab's `sessionStorage`, strips it from the
address bar, and sends it as `Authorization: Bearer <token>` on every write.
The server compares it in constant time.

**Guards on every request:**

- `--write` with a non-loopback `--addr` is a hard error.
- `Host` must be `127.0.0.1:<port>` or `localhost:<port>` (DNS rebinding).
- Every write must carry an `Origin` equal to the server's own (cross-site
  posts). The server sends no CORS headers.
- Read endpoints (`/api/ledger`, `/api/seal`) keep their current behaviour
  and need no token.

**Attribution** is unchanged: the UI's adjudication is the same signed entry
the CLI writes, `--by` defaults to the OS user, and the ledger format does
not change.

**The honest limit.** The token narrows "who can write" from "any local
process" to "whoever launched this server". An agent that launches
`corral ui --write` itself, or reads the launching terminal, can write. That
is no weaker than today (any local agent can already run
`corral review adjudicate`), but it is not proof of a human either. This
paragraph goes in the `-h` text and the docs. To close the agent side,
`skills/corral/SKILL.md` and `AGENTS.md` gain an explicit rule: **agents
never start `corral ui --write`.**

## 2. Triage and adjudicate

**Needs your verdict.** Every finding across every review with no human
adjudication, newest review first, grouped by review. Each item leads with
the claim, file:line, tier, severity, reviewer, verifier and the verifier's
answer; a reproduced finding also shows its script, exit code and recorded
output.

**Does it still reproduce: by execution.** The UI never shows "fixed" on the
strength of a commit message. A reproduced finding gets a **Re-run on HEAD**
button backed by a new CLI verb:

    corral review recheck <ledger> <hash>#Rn [--repo <dir>]

It runs the recorded script against the current commit in a disposable
worktree, the way `corral review` runs it, and reports one of:

- **still reproduces** (script exited 0)
- **no longer reproduces** (any other exit)
- **could not run** (a missing toolchain, a timeout), which is never
  reported as fixed

It writes nothing to the ledger. A person who wants the result on the record
quotes it in their reason.

Code-read and hypothesis findings have no script. They show "no script: judge
from the code" and a link to file:line at the review's commit.

**Verdict form.** Confirm / Refute, a required reason, and an editable
suggestion built only from evidence: the recheck result if there is one, the
verifier's answer, and a line like "cited as fixed by <commit>" when a
`git log` message names the review's hash. That last line is labelled "cited
by a commit message, not verified" unless a recheck also says "no longer
reproduces".

Submitting runs `corral review adjudicate` as a subprocess. The UI shows the
CLI's own output and reloads the ledger. On error it shows the CLI's message
verbatim and changes nothing.

**Confirmed, still open.** Confirmed findings whose recheck still reproduces,
or that have no script, sit in their own list: the input to the next fix
batch.

## 3. Publish

Shown only when the ledger directory is a git checkout. For a plain
directory the panel says "not a git checkout; nothing to publish".

**What is not on the remote, listed by file name:**

- **Uncommitted** entries in the directory. An entry this server did not
  write in this session is flagged "present before this session, origin
  unknown". This is the untracked-entries case made visible.
- **Unpushed** commits ahead of `origin`.

**Publish** runs the manual sequence, showing each step's command and output:

1. `git fetch`. If `origin` moved while there are unpushed entries, **stop**.
   Never rebase automatically: re-linking re-hashes entries and would orphan
   adjudications that name the old hashes. Point to the CLI procedure.
2. `corral ledger verify`, gated on its own exit code. On failure, nothing
   is committed or pushed, and the failing entry is shown.
3. `git commit`, with a generated, editable message ("adjudications: 3
   confirmed, 1 refuted").
4. A confirmation: "Push N entries to origin/corral/ledger? This is public."
5. `git push`, then re-verify a fresh copy of the remote branch (`git
   archive` into a temp dir, `corral ledger verify` on it) and show the
   result.

Push uses the operator's own git credentials through the `git` binary; the
UI never holds a token. There is no force-push path: no button, and the code
never passes `--force`.

## 4. Run a round

**New round form:**

- **Scope** comes from `corral review plan` (never reviewed, then changed
  since review, then stalest), with its proposal preselected. The operator
  chooses; the planner's rule is that a person names the scope.
- **Reviewer and verifier** list the defined agent seats (`claude-code`,
  `codex`, any `CORRALAI_AGENT_<NAME>`) and registry aliases, each with a
  model pin. Only what the CLI refuses is refused (reviewer = verifier).
  Two warnings are shown and not enforced: "this reviewer reviewed this scope
  last time", and "reviewer and verifier are the same vendor".
- **Before Start**, it states the cost: which subscription or key each seat
  uses, and how long the last round on this scope took, when the ledger
  knows.

**Running:**

- Start runs `corral review …` as a subprocess. The page shows the exact
  command line.
- One round at a time: the ledger has one writer, so a second Start is
  refused while one runs.
- Output streams to the page (server-sent events) and survives a reload: the
  server keeps the running output and the page reconnects.
- Cancel stops the round. Stopping `corral ui` also cancels it. `corral
  review` writes its entry only when it finishes, so a cancelled round
  writes nothing, and the page says so.
- On completion the new review appears in the verdict queue, and its entry
  appears under Publish as uncommitted.

**Guards:** Start and Cancel need the launch token. Seats run exactly as
from the CLI (`codex` keeps `--sandbox read-only` in a disposable copy); the
UI grants no new agent permissions.

## Out of scope

- Fixing findings. That stays with coding agents and pull requests (the
  auditor never builds).
- Scheduling rounds. A person starts each one; the loop has a stopping rule
  and it is a person.
- Accounts, remote access, several operators, parallel rounds.

## Testing

Every test below is written to fail before the code it covers exists.

- **Write authority:** each write endpoint refuses (a) no token, (b) a wrong
  token, (c) a foreign `Host`, (d) a missing or foreign `Origin`; `--write`
  with `--addr 0.0.0.0:…` exits non-zero; read endpoints still work without
  a token; the token never appears in any response body or log line.
- **Recheck:** a recorded script exiting 0 reports "still reproduces", a
  non-zero exit reports "no longer reproduces", a timeout or missing
  interpreter reports "could not run" and never "no longer reproduces", and
  the ledger directory is byte-identical afterwards.
- **Adjudicate:** a UI adjudication produces an entry that `corral ledger
  verify` accepts and that is indistinguishable from the CLI's for the same
  input; a CLI refusal surfaces verbatim and writes nothing.
- **Publish:** verify failure prevents commit and push (checked with a
  deliberately broken entry); a remote that moved while entries are unpushed
  stops before commit; untracked entries from before the session are listed
  and flagged; no code path passes `--force` (a test greps the invocation).
- **Run a round:** a second Start while one runs is refused; cancelling
  mid-round leaves the ledger unchanged; a finished round's entry verifies.
  These use a fake agent seat defined via `CORRALAI_AGENT_<NAME>`, so no
  model is called.

## Build order

Each piece ships on its own and depends on the one before:

1. Write authority (`--write`, the token, the guards, the agent rule in the
   skill and AGENTS.md)
2. `corral review recheck`, then the verdict queue and form
3. Publish
4. Run a round
