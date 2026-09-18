# agent-issues

A local issue ledger for locally developed tools, shared across projects.

An issue is filed against a **component** — a tool, service, or repository — by
whoever hit the defect. That is usually an agent session working in a *different*
project from the one at fault. Repeat sightings of the same fingerprint collapse
onto one issue and raise its occurrence count; a sighting after a close records a
recurrence rather than silently reopening.

## Install

Requires Go 1.27.1 or newer (`go.mod`). The repository is private, so install
from a clone rather than by module path:

```bash
git clone https://github.com/osteele/agent-issues
cd agent-issues
just install          # go install ./cmd/issues
```

That puts `issues` in `$(go env GOBIN)` — usually `~/go/bin`, which must be on
your PATH. `just build` instead produces `./issues` in the working directory.

## Use

```bash
issues component add weft --prefix wb --path ~/code/research-tools/weft
issues report --component weft \
  --title "runner pending job is missing queue payload" \
  --kind invariant --severity error \
  --fingerprint "queue.missing_payload:studio" \
  --ref "wj2454 on studio" \
  --summary "Queue state is inconsistent; not a job-code or input problem." \
  --detail "Raw evidence, paths, command output, invariant details."
# Filed wb147 (weft): runner pending job is missing queue payload

issues note wb147 "Seen again after a runner restart; terminal artifacts existed."
issues list                      # open, plus closed issues still actively recurring
                                 # (2+ recurrences, most recent within 7 days)
issues list --component .        # just the component you are standing in
issues show wb147
issues close wb147 --reason "fixed in jj rev <change-id> (<git-commit>)"
```

## Why a ledger rather than mail or a per-repo tracker

Reporting a defect as a message to a live session works only while that session
is alive, cannot dedupe, and has no state beyond read/unread — so an unfixed
defect's entire persistent record is "unread". A ledger gives it open/closed, an
occurrence count, and a recurrence signal when a fix does not hold.

The store is **shared, not per-repository**, because the filer is in project A
and the target is project B. A per-repository store would force the filer to
locate and open a database it has no other business touching.

## Privacy

The ledger is local and is never synchronized. Issue text routinely carries job
identifiers, unpublished research description, and absolute local paths, so
publishing is an explicit per-issue decision:

```bash
issues publish wb147                    # prints exactly what would be sent, sends nothing
issues publish wb147 --yes              # summary, kind, severity, likelihood,
                                        # occurrence count, and scope if set
issues publish wb147 --include-detail --include-ref --include-notes --yes
```

`detail`, `ref` and notes are the fields that hold local specifics, and each is
opt-in. `scope` is published by default and is free-form, so keep it a
classification label rather than a place for detail. Nothing is redacted for
you — review the printed body before `--yes`.

## Layout

| Path | |
|---|---|
| `~/.local/share/agent-issues/issues.db` | the ledger (override with `$AGENT_ISSUES_DB`) |
| `internal/store` | schema, dedupe and recurrence semantics |
| `internal/cli` | the `issues` command line |

`$AGENT_ISSUES_REPORTER` stamps a session name on everything it files.

## weft

weft's bug tracker was the origin of this design, and weft's history lives here
now — all 146 issues, with their numbers preserved, so `wb64` still resolves and
every citation in a lab notebook still points at the same issue.

`weft bug ...` forwards here (weft config `bug.tracker = "issues"`, the default).
Those commands keep working; new work should use `issues` directly.

```bash
issues import-weft              # idempotent; adds what is new
issues import-weft --reconcile  # also refresh already-imported issues from the source
```
