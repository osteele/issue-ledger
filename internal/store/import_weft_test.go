package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// writeWeftBugDB builds a ledger with weft's schema so the importer is
// exercised against the real column set rather than a convenient subset.
func writeWeftBugDB(t *testing.T, path string, withRecurrence bool) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer database.Close()

	recurrenceCols := ""
	if withRecurrence {
		recurrenceCols = `,
			recurrences INTEGER NOT NULL DEFAULT 0,
			last_recurrence_at INTEGER NOT NULL DEFAULT 0`
	}
	if _, err := database.Exec(`
		CREATE TABLE bugs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			status TEXT NOT NULL DEFAULT 'open',
			title TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'bug',
			scope TEXT NOT NULL DEFAULT 'infrastructure',
			likelihood TEXT NOT NULL DEFAULT 'unknown',
			severity TEXT NOT NULL DEFAULT 'notice',
			fingerprint TEXT NOT NULL DEFAULT '',
			job_id INTEGER,
			host TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			occurrences INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			closed_at INTEGER,
			close_reason TEXT NOT NULL DEFAULT ''` + recurrenceCols + `
		);
		CREATE TABLE bug_notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			bug_id INTEGER NOT NULL REFERENCES bugs(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		INSERT INTO bugs (id, status, title, fingerprint, job_id, host, summary, detail,
		                  occurrences, created_at, updated_at, closed_at, close_reason)
		VALUES (64, 'closed', 'cost derived from reconcile time', 'cost.reconcile', 6066, 'wi3626',
		        'billing bookkeeping defect', 'ended_at stamped at reconcile time',
		        3, 100, 200, 250, 'fixed');
		INSERT INTO bugs (id, status, title, fingerprint, created_at, updated_at)
		VALUES (139, 'open', 'publication surface', 'pub.surface', 300, 400);
		INSERT INTO bug_notes (bug_id, body, created_at) VALUES (64, 'seen on studio', 150);
		INSERT INTO bug_notes (bug_id, body, created_at) VALUES (64, 'true cost $3.24', 160);
	`); err != nil {
		t.Fatalf("seed source: %v", err)
	}
}

func TestImportWeftBugsPreservesNumbersAndNotes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, true)

	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	result, err := ImportWeftBugs(database, source, "weft", false)
	if err != nil {
		t.Fatalf("ImportWeftBugs: %v", err)
	}
	if result.Imported != 2 || result.Notes != 2 {
		t.Fatalf("imported %d issues / %d notes, want 2 / 2", result.Imported, result.Notes)
	}

	// The whole point of preserving numbers: wb64 and wb139 are cited in lab
	// notebooks and review ledgers, and must still resolve.
	wb64, err := GetIssue(database, "wb64")
	if err != nil {
		t.Fatalf("GetIssue(wb64): %v", err)
	}
	if wb64.Status != "closed" || wb64.CloseReason != "fixed" {
		t.Fatalf("wb64 = %+v, want the closed verdict preserved", wb64)
	}
	if wb64.Occurrences != 3 {
		t.Fatalf("wb64 occurrences = %d, want 3", wb64.Occurrences)
	}
	if wb64.ClosedAt == nil || *wb64.ClosedAt != 250 {
		t.Fatalf("wb64 closed_at not preserved: %+v", wb64.ClosedAt)
	}
	if wb64.CreatedAt != 100 {
		t.Fatalf("wb64 created_at = %d, want the original 100", wb64.CreatedAt)
	}
	// weft's job_id and host fold into the generic ref, keeping the job id in
	// the form it is cited as.
	if wb64.Ref != "wj6066 on wi3626" {
		t.Fatalf("wb64 ref = %q", wb64.Ref)
	}

	if _, err := GetIssue(database, "wb139"); err != nil {
		t.Fatalf("GetIssue(wb139): %v", err)
	}

	notes, err := ListNotes(database, wb64.ID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 2 || notes[0].Body != "seen on studio" {
		t.Fatalf("notes = %+v", notes)
	}

	// A new issue filed after the import must not reuse an imported number.
	next := mustReport(t, database, Report{Component: "weft", Title: "new", Fingerprint: "new"})
	if next.CitableID() != "wb140" {
		t.Fatalf("next issue = %s, want wb140", next.CitableID())
	}
}

func TestImportWeftBugsIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, true)

	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	if _, err := ImportWeftBugs(database, source, "weft", false); err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := ImportWeftBugs(database, source, "weft", false)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if second.Imported != 0 || second.Skipped != 2 {
		t.Fatalf("second import imported %d / skipped %d, want 0 / 2", second.Imported, second.Skipped)
	}
	all, err := ListIssues(database, ListOptions{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ledger holds %d issues after a double import, want 2", len(all))
	}
	notes, err := ListNotes(database, all[len(all)-1].ID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) > 2 {
		t.Fatalf("notes were duplicated by the second import: %d", len(notes))
	}
}

// A bugs.db written before weft added recurrence tracking must still import.
func TestImportWeftBugsHandlesPreRecurrenceSchema(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, false)

	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	result, err := ImportWeftBugs(database, source, "weft", false)
	if err != nil {
		t.Fatalf("ImportWeftBugs: %v", err)
	}
	if result.Imported != 2 {
		t.Fatalf("imported %d, want 2", result.Imported)
	}
	wb64, err := GetIssue(database, "wb64")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if wb64.Recurrences != 0 {
		t.Fatalf("recurrences = %d, want 0 for a pre-recurrence source", wb64.Recurrences)
	}
}

func TestImportWeftBugsRefusesToOverwriteAnExistingNumber(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, true)

	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	// Seed wb1..wb64 by hand so the import collides on 64.
	if _, err := database.Exec(`
		INSERT INTO issues (component, number, status, title, fingerprint, occurrences, created_at, updated_at)
		VALUES ('weft', 64, 'open', 'locally filed', 'local', 1, 1, 1)`); err != nil {
		t.Fatalf("seed clash: %v", err)
	}
	if _, err := ImportWeftBugs(database, source, "weft", false); err == nil {
		t.Fatal("import should refuse rather than renumber over an existing issue")
	}
}

func TestImportWeftBugsRejectsUnknownComponent(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, true)
	database := testLedger(t)
	if _, err := ImportWeftBugs(database, source, "weft", false); err == nil {
		t.Fatal("import into an unregistered component should fail")
	}
}

func TestImportWeftBugsReconcilesStatusAndNewNotes(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "bugs.db")
	writeWeftBugDB(t, source, true)

	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	if _, err := ImportWeftBugs(database, source, "weft", false); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// wb139 was open at import time. The old ledger keeps receiving writes
	// during a cutover, so it is closed there afterwards, with a new note.
	legacy, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	if _, err := legacy.Exec(`
		UPDATE bugs SET status = 'closed', closed_at = 500, close_reason = 'fixed in 07042274',
		                occurrences = 4, updated_at = 500 WHERE id = 139;
		INSERT INTO bug_notes (bug_id, body, created_at) VALUES (139, 'later evidence', 450);
	`); err != nil {
		t.Fatalf("mutate source: %v", err)
	}
	legacy.Close()

	// Without --reconcile the stale state persists: that is the gap.
	plain, err := ImportWeftBugs(database, source, "weft", false)
	if err != nil {
		t.Fatalf("plain re-import: %v", err)
	}
	if plain.Reconciled != 0 || plain.Skipped != 2 {
		t.Fatalf("plain re-import reconciled %d / skipped %d, want 0 / 2", plain.Reconciled, plain.Skipped)
	}
	stale, err := GetIssue(database, "wb139")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if stale.Status != "open" {
		t.Fatalf("status = %q, want the stale open state without --reconcile", stale.Status)
	}

	result, err := ImportWeftBugs(database, source, "weft", true)
	if err != nil {
		t.Fatalf("reconciling import: %v", err)
	}
	if result.Reconciled != 2 || result.Imported != 0 {
		t.Fatalf("reconciled %d / imported %d, want 2 / 0", result.Reconciled, result.Imported)
	}
	if result.Notes != 1 {
		t.Fatalf("appended %d notes, want only the new one", result.Notes)
	}

	reconciled, err := GetIssue(database, "wb139")
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if reconciled.Status != "closed" || reconciled.CloseReason != "fixed in 07042274" {
		t.Fatalf("wb139 = %+v, want the source's close verdict", reconciled)
	}
	if reconciled.ClosedAt == nil || *reconciled.ClosedAt != 500 {
		t.Fatalf("closed_at not carried over: %v", reconciled.ClosedAt)
	}
	if reconciled.Occurrences != 4 {
		t.Fatalf("occurrences = %d, want 4", reconciled.Occurrences)
	}

	notes, err := ListNotes(database, reconciled.ID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Body != "later evidence" {
		t.Fatalf("notes = %+v, want exactly the one new note", notes)
	}

	// A third reconciling pass must be a no-op for notes.
	third, err := ImportWeftBugs(database, source, "weft", true)
	if err != nil {
		t.Fatalf("third import: %v", err)
	}
	if third.Notes != 0 {
		t.Fatalf("third pass appended %d notes, want 0", third.Notes)
	}
}
