# Decision log

One-line Y-statements for reversible decisions. Consequential, hard-to-reverse
choices get a numbered record in this directory instead.

- 2026-09-17 — In the context of defect reports that agents file across project
  boundaries, facing the fact that mail to a named session dies with that session
  and cannot dedupe, we chose a **single shared local ledger keyed by component**
  over a per-repository store (itrack, git-bug, beads), to accept that the ledger
  is a third coordination store alongside agent-mail and lore, because the filer
  works in a different repository from the target and a per-repository store
  would require it to open a database it has no other business touching.
- 2026-09-17 — In the context of these repositories being public on GitHub, we
  chose a **local-only ledger with publishing as an explicit per-issue export**
  over GitHub issues as the storage backend, to accept that issues are not
  shareable or backed up by default, because issue text routinely carries job
  ids, unpublished research description and local paths, and weft had already
  rejected its own GitHub backend in practice for this reason.
- 2026-09-17 — In the context of migrating weft's 146 bugs, we chose to
  **preserve each issue's number under a per-component prefix** over renumbering
  into one global sequence, to accept per-component number allocation and an
  immutable prefix per component, because `wb64` and its siblings are cited
  across lab notebooks, review ledgers, findings and source comments.
- 2026-09-17 — In the context of retiring `weft bug`, we chose to **proxy it to
  the `issues` CLI** over removing it, to accept a shim in weft and a PATH
  dependency, because skills and habit still invoke it and a transitional period
  is cheaper than a flag day.
