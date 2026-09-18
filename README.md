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
go install ./cmd/issues
issues --help            # verify
```

That installs a binary named `issues` into `$GOBIN`, or `$GOPATH/bin` when
`GOBIN` is unset — `~/go/bin` on a default setup. That directory must be on
your PATH.

If you have [`just`](https://github.com/casey/just), `just install` runs the
same command, and `just build` produces `./issues` in the working directory
instead. It is a convenience, not a requirement.

## Use

Register each tool you want to file issues against once. `--path` is your own
local checkout of that tool, and is what lets `--component .` later resolve the
directory you are standing in:

```bash
issues component add myproject --prefix mp --path ~/src/myproject

issues report --component myproject \
  --title "runner pending job is missing queue payload" \
  --kind invariant --severity error \
  --fingerprint "queue.missing_payload:studio" \
  --ref "build 2454 on the CI host" \
  --summary "Queue state is inconsistent; not a job-code or input problem." \
  --detail "Raw evidence, paths, command output, invariant details."
# Filed mp1 (myproject): runner pending job is missing queue payload

issues note mp1 "Seen again after a runner restart; terminal artifacts existed."
issues list                      # open, plus closed issues still actively recurring
                                 # (2+ recurrences, most recent within 7 days)
issues list --component .        # just the component you are standing in
issues show mp1
issues close mp1 --reason "fixed in <commit>"
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

Publishing needs a target repository, taken from the component unless you pass
`--repo`:

```bash
issues component set myproject --repo owner/myproject

issues publish mp1                    # prints exactly what would be sent, sends nothing
issues publish mp1 --yes              # summary, kind, severity, likelihood,
                                      # occurrence count, and scope if set
issues publish mp1 --include-detail --include-ref --include-notes --yes
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

## Migrating from weft (skip unless you used it)

weft is a research job runner, and its built-in bug tracker was the origin of
this design. This section is migration material for an existing weft user;
nothing here is needed to adopt agent-issues.

weft's history lives here now — all 146 issues, with their numbers preserved, so `wb64` still resolves and
every citation in a lab notebook still points at the same issue.

`weft bug ...` forwards here (weft config `bug.tracker = "issues"`, the default).
Those commands keep working; new work should use `issues` directly.

```bash
issues import-weft              # idempotent; adds what is new
issues import-weft --reconcile  # also refresh already-imported issues from the source
```

## Status and further reference

In daily use, and the sole tracker for its first component since the weft
cutover in September 2026 (146 issues migrated). The store format is settled;
the CLI may still gain flags.

Every command self-documents — start with `issues report --help` for the
fingerprint and severity options, and `issues publish --help` for exactly what
an export sends. Decisions and their rejected alternatives are in
`docs/decisions/log.md`.
