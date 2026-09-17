package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func testLedger(t *testing.T) *sql.DB {
	t.Helper()
	database, err := OpenAt(filepath.Join(t.TempDir(), "issues.db"))
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func mustRegister(t *testing.T, database *sql.DB, name, prefix string) {
	t.Helper()
	if _, err := RegisterComponent(database, Component{Name: name, Prefix: prefix}); err != nil {
		t.Fatalf("RegisterComponent(%q): %v", name, err)
	}
}

func mustReport(t *testing.T, database *sql.DB, report Report) *Issue {
	t.Helper()
	issue, _, err := ReportIssue(database, report)
	if err != nil {
		t.Fatalf("ReportIssue(%q): %v", report.Title, err)
	}
	return issue
}

func TestReportingSameFingerprintUpdatesOneIssue(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	first, created, err := ReportIssue(database, Report{
		Component: "weft", Title: "queue payload missing", Fingerprint: "queue.missing_payload",
	})
	if err != nil {
		t.Fatalf("first report: %v", err)
	}
	if !created {
		t.Fatal("first report should create an issue")
	}

	second, created, err := ReportIssue(database, Report{
		Component: "weft", Title: "queue payload missing again", Fingerprint: "queue.missing_payload",
		Summary: "refreshed summary",
	})
	if err != nil {
		t.Fatalf("second report: %v", err)
	}
	if created {
		t.Fatal("second report with the same fingerprint must not create a second issue")
	}
	if second.Number != first.Number {
		t.Fatalf("expected the same issue, got %s then %s", first.CitableID(), second.CitableID())
	}
	if second.Occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2", second.Occurrences)
	}
	if second.Summary != "refreshed summary" {
		t.Fatalf("summary = %q, want the refreshed value", second.Summary)
	}
	// The original title stands: the first framing of a fault is usually the
	// considered one, and later sightings should not silently retitle it.
	if second.Title != "queue payload missing" {
		t.Fatalf("title = %q, want the original", second.Title)
	}
}

func TestFingerprintDedupeIsPerComponent(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	mustRegister(t, database, "agent-mail", "am")

	a := mustReport(t, database, Report{Component: "weft", Title: "timeout", Fingerprint: "timeout"})
	b := mustReport(t, database, Report{Component: "agent-mail", Title: "timeout", Fingerprint: "timeout"})

	if a.CitableID() == b.CitableID() {
		t.Fatalf("same fingerprint in two components collapsed onto %s", a.CitableID())
	}
	if a.CitableID() != "wb1" || b.CitableID() != "am1" {
		t.Fatalf("got %s and %s, want wb1 and am1", a.CitableID(), b.CitableID())
	}
}

func TestNumbersAreSequentialPerComponent(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	mustRegister(t, database, "agent-mail", "am")

	mustReport(t, database, Report{Component: "weft", Title: "one", Fingerprint: "f1"})
	mustReport(t, database, Report{Component: "agent-mail", Title: "two", Fingerprint: "f2"})
	third := mustReport(t, database, Report{Component: "weft", Title: "three", Fingerprint: "f3"})

	// agent-mail's issue must not consume a weft number.
	if third.CitableID() != "wb2" {
		t.Fatalf("third issue = %s, want wb2", third.CitableID())
	}
}

func TestReportingAClosedFingerprintRecordsRecurrenceWithoutReopening(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	issue := mustReport(t, database, Report{Component: "weft", Title: "stale mount", Fingerprint: "mount.stale"})
	if _, err := CloseIssue(database, issue.CitableID(), "fixed in 902a1dc"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}

	_, created, err := ReportIssue(database, Report{
		Component: "weft", Title: "stale mount", Fingerprint: "mount.stale", Note: "seen again",
	})
	if err == nil {
		t.Fatal("reporting a closed fingerprint should return FingerprintClosedError")
	}
	if !IsFingerprintClosed(err) {
		t.Fatalf("error = %v, want FingerprintClosedError", err)
	}
	if created {
		t.Fatal("a closed-fingerprint report must not create a new issue")
	}

	reloaded, err := GetIssue(database, issue.CitableID())
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if reloaded.Status != "closed" {
		t.Fatalf("status = %q, want closed: reporting must not reopen", reloaded.Status)
	}
	if reloaded.Recurrences != 1 {
		t.Fatalf("recurrences = %d, want 1", reloaded.Recurrences)
	}
	if reloaded.LastRecurrenceAt == 0 {
		t.Fatal("last_recurrence_at was not stamped")
	}
	// The recurrence must be committed even though the call returned an error.
	notes, err := ListNotes(database, reloaded.ID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 1 || notes[0].Body != "seen again" {
		t.Fatalf("notes = %+v, want the note from the recurring report", notes)
	}
}

func TestClosingResetsRecurrenceCounters(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	issue := mustReport(t, database, Report{Component: "weft", Title: "flake", Fingerprint: "flake"})
	id := issue.CitableID()

	if _, err := CloseIssue(database, id, "first attempt"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	for range 3 {
		if _, _, err := ReportIssue(database, Report{Component: "weft", Title: "flake", Fingerprint: "flake"}); !IsFingerprintClosed(err) {
			t.Fatalf("expected FingerprintClosedError, got %v", err)
		}
	}
	if _, err := ReopenIssue(database, id); err != nil {
		t.Fatalf("ReopenIssue: %v", err)
	}
	if _, err := CloseIssue(database, id, "second attempt"); err != nil {
		t.Fatalf("second CloseIssue: %v", err)
	}

	reloaded, err := GetIssue(database, id)
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	// Counters describe the interval since the CURRENT close. Carrying the
	// three earlier recurrences across would resurface this issue immediately
	// after a close, with nothing having happened since that close.
	if reloaded.Recurrences != 0 || reloaded.LastRecurrenceAt != 0 {
		t.Fatalf("recurrences = %d / lastAt = %d after reclose, want 0 / 0",
			reloaded.Recurrences, reloaded.LastRecurrenceAt)
	}
}

func TestReopenRefusesWhenAnotherOpenIssueHoldsTheFingerprint(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	first := mustReport(t, database, Report{Component: "weft", Title: "dup", Fingerprint: "shared"})
	if _, err := CloseIssue(database, first.CitableID(), ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	// Reporting the closed fingerprint cannot create a second open issue, so
	// build the conflict the way it really arises: a different fingerprint is
	// filed, then edited to collide. Reopening must refuse rather than violate
	// the one-open-issue-per-fingerprint invariant.
	second := mustReport(t, database, Report{Component: "weft", Title: "other", Fingerprint: "other"})
	if _, err := database.Exec(`UPDATE issues SET fingerprint = 'shared' WHERE id = ?`, second.ID); err != nil {
		t.Fatalf("seed conflict: %v", err)
	}

	if _, err := ReopenIssue(database, first.CitableID()); err == nil {
		t.Fatal("reopen should refuse when an open issue holds the same fingerprint")
	}
	reloaded, err := GetIssue(database, first.CitableID())
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if reloaded.Status != "closed" {
		t.Fatalf("status = %q, want it left closed", reloaded.Status)
	}
}

func TestDefaultListingSurfacesOnlyPersistentRecurrences(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	straggler := mustReport(t, database, Report{Component: "weft", Title: "straggler", Fingerprint: "one"})
	persistent := mustReport(t, database, Report{Component: "weft", Title: "persistent", Fingerprint: "many"})
	stale := mustReport(t, database, Report{Component: "weft", Title: "stale", Fingerprint: "old"})
	for _, issue := range []*Issue{straggler, persistent, stale} {
		if _, err := CloseIssue(database, issue.CitableID(), ""); err != nil {
			t.Fatalf("CloseIssue: %v", err)
		}
	}

	report := func(fingerprint string, times int) {
		for range times {
			if _, _, err := ReportIssue(database, Report{Component: "weft", Title: "x", Fingerprint: fingerprint}); !IsFingerprintClosed(err) {
				t.Fatalf("expected FingerprintClosedError for %q, got %v", fingerprint, err)
			}
		}
	}
	report("one", 1)  // one straggler: below the count floor
	report("many", 2) // at the count floor
	report("old", 3)  // above the count floor but aged out below

	old := time.Now().Add(-8 * 24 * time.Hour).Unix()
	if _, err := database.Exec(`UPDATE issues SET last_recurrence_at = ? WHERE id = ?`, old, stale.ID); err != nil {
		t.Fatalf("age the stale recurrence: %v", err)
	}

	issues, err := ListIssues(database, ListOptions{})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	listed := map[string]bool{}
	for _, issue := range issues {
		listed[issue.Title] = true
	}
	if !listed["persistent"] {
		t.Error("a closed issue recurring at the count floor should be listed")
	}
	if listed["straggler"] {
		t.Error("a single post-close straggler should not resurface a fixed issue")
	}
	if listed["stale"] {
		t.Error("a recurrence older than the recency floor should not be listed")
	}

	all, err := ListIssues(database, ListOptions{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListIssues(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("--all listed %d issues, want 3", len(all))
	}
}

func TestListFiltersByComponent(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	mustRegister(t, database, "agent-mail", "am")
	mustReport(t, database, Report{Component: "weft", Title: "w", Fingerprint: "a"})
	mustReport(t, database, Report{Component: "agent-mail", Title: "m", Fingerprint: "b"})

	issues, err := ListIssues(database, ListOptions{Component: "agent-mail"})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	if len(issues) != 1 || issues[0].Component != "agent-mail" {
		t.Fatalf("component filter returned %+v", issues)
	}
}

func TestReportRequiresComponentAndTitle(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	if _, _, err := ReportIssue(database, Report{Component: "weft"}); err == nil {
		t.Error("a report without a title should fail")
	}
	if _, _, err := ReportIssue(database, Report{Title: "orphan"}); err == nil {
		t.Error("a report without a component should fail")
	}
	_, _, err := ReportIssue(database, Report{Component: "never-registered", Title: "x"})
	var unknown *UnknownComponentError
	if !errors.As(err, &unknown) {
		t.Errorf("error = %v, want UnknownComponentError", err)
	}
}

func TestFingerprintDefaultsToTitle(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	first := mustReport(t, database, Report{Component: "weft", Title: "same words"})
	second := mustReport(t, database, Report{Component: "weft", Title: "same words"})
	if first.CitableID() != second.CitableID() {
		t.Fatalf("an identical title should dedupe: got %s then %s", first.CitableID(), second.CitableID())
	}
	if second.Occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2", second.Occurrences)
	}
}

func TestNotesAreAppendOnlyAndOrdered(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	issue := mustReport(t, database, Report{Component: "weft", Title: "t", Fingerprint: "f", Note: "first"})
	if _, err := AddNote(database, issue.CitableID(), "second", "code-proud-axolotl"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	notes, err := ListNotes(database, issue.ID)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if len(notes) != 2 || notes[0].Body != "first" || notes[1].Body != "second" {
		t.Fatalf("notes = %+v, want first then second", notes)
	}
	if notes[1].Author != "code-proud-axolotl" {
		t.Fatalf("author = %q", notes[1].Author)
	}
	if _, err := AddNote(database, issue.CitableID(), "   ", ""); err == nil {
		t.Error("an empty note should be rejected")
	}
	if _, err := AddNote(database, "wb999", "orphan", ""); err == nil {
		t.Error("a note against a missing issue should fail")
	}
}

func TestIDFormatAndParse(t *testing.T) {
	for _, tc := range []struct {
		in     string
		prefix string
		number int64
	}{
		{"wb139", "wb", 139},
		{"WB139", "wb", 139},
		{" am7 ", "am", 7},
	} {
		prefix, number, err := ParseID(tc.in)
		if err != nil {
			t.Errorf("ParseID(%q): %v", tc.in, err)
			continue
		}
		if prefix != tc.prefix || number != tc.number {
			t.Errorf("ParseID(%q) = %q/%d, want %q/%d", tc.in, prefix, number, tc.prefix, tc.number)
		}
		if got := FormatID(prefix, number); got != tc.prefix+itoa(tc.number) {
			t.Errorf("FormatID round trip = %q", got)
		}
	}
	for _, bad := range []string{"", "139", "wb", "wb0", "wb-1", "toolongprefix1", "wb12x"} {
		if _, _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) should fail", bad)
		}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestGetIssueRejectsUnknownID(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	if _, err := GetIssue(database, "wb1"); err == nil {
		t.Error("an id with no row should fail")
	}
	if _, err := GetIssue(database, "zz1"); err == nil {
		t.Error("an id with an unregistered prefix should fail")
	}
}

func TestComponentRegistration(t *testing.T) {
	database := testLedger(t)

	component, err := RegisterComponent(database, Component{Name: "agent-mail"})
	if err != nil {
		t.Fatalf("RegisterComponent: %v", err)
	}
	if component.Prefix != "am" {
		t.Fatalf("derived prefix = %q, want am", component.Prefix)
	}
	if _, err := RegisterComponent(database, Component{Name: "agent-mail"}); err == nil {
		t.Error("re-registering a component should fail")
	}
	// agent-metrics would derive "am" too; the collision must be reported
	// rather than silently reassigned, because a prefix is how ids resolve.
	if _, err := RegisterComponent(database, Component{Name: "agent-metrics"}); err == nil {
		t.Error("a colliding derived prefix should fail")
	}
	if _, err := RegisterComponent(database, Component{Name: "agent-metrics", Prefix: "ag"}); err != nil {
		t.Errorf("an explicit prefix should resolve the collision: %v", err)
	}
	if _, err := RegisterComponent(database, Component{Name: "Not A Name"}); err == nil {
		t.Error("an invalid component name should fail")
	}
	if _, err := RegisterComponent(database, Component{Name: "ok", Prefix: "TOOLONG"}); err == nil {
		t.Error("an invalid prefix should fail")
	}
}

func TestDerivePrefix(t *testing.T) {
	for name, want := range map[string]string{
		"weft":            "we",
		"agent-mail":      "am",
		"agent-lore":      "al",
		"research-rounds": "rr",
		"labbook":         "la",
		"a-b-c-d-e-f":     "abcde",
	} {
		if got := DerivePrefix(name); got != want {
			t.Errorf("DerivePrefix(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCloseIsIdempotentlyRejectedAndReopenRequiresClosed(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	issue := mustReport(t, database, Report{Component: "weft", Title: "t", Fingerprint: "f"})

	if _, err := ReopenIssue(database, issue.CitableID()); err == nil {
		t.Error("reopening an open issue should fail")
	}
	if _, err := CloseIssue(database, issue.CitableID(), "done"); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	if _, err := CloseIssue(database, issue.CitableID(), "again"); err == nil {
		t.Error("closing an already closed issue should fail")
	}
	reloaded, err := GetIssue(database, issue.CitableID())
	if err != nil {
		t.Fatalf("GetIssue: %v", err)
	}
	if reloaded.CloseReason != "done" {
		t.Fatalf("close reason = %q, want the first one preserved", reloaded.CloseReason)
	}
}

func TestOpenFingerprintUniquenessIsEnforcedByTheDatabase(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")
	mustReport(t, database, Report{Component: "weft", Title: "a", Fingerprint: "shared"})
	other := mustReport(t, database, Report{Component: "weft", Title: "b", Fingerprint: "distinct"})

	// The invariant must hold against a direct write, not only against the
	// write path that maintains it.
	_, err := database.Exec(`UPDATE issues SET fingerprint = 'shared' WHERE id = ?`, other.ID)
	if err == nil {
		t.Fatal("two open issues in one component were allowed to share a fingerprint")
	}
}

// Ordering is part of the listing's contract: `issues list` is how a session
// finds what is outstanding, so an open issue must never be buried under closed
// history. A map of titles cannot see this, which is why it has its own test.
func TestListingPutsOpenIssuesFirstThenNewestActivity(t *testing.T) {
	database := testLedger(t)
	mustRegister(t, database, "weft", "wb")

	// A closed issue that is still recurring, with activity NEWER than the
	// open ones — the case that decides whether status or recency leads.
	recurring := mustReport(t, database, Report{Component: "weft", Title: "recurring closed", Fingerprint: "recur"})
	if _, err := CloseIssue(database, recurring.CitableID(), ""); err != nil {
		t.Fatalf("CloseIssue: %v", err)
	}
	for range 2 {
		if _, _, err := ReportIssue(database, Report{Component: "weft", Title: "x", Fingerprint: "recur"}); !IsFingerprintClosed(err) {
			t.Fatalf("expected FingerprintClosedError, got %v", err)
		}
	}

	older := mustReport(t, database, Report{Component: "weft", Title: "older open", Fingerprint: "a"})
	newer := mustReport(t, database, Report{Component: "weft", Title: "newer open", Fingerprint: "b"})
	now := time.Now().Unix()
	if _, err := database.Exec(`UPDATE issues SET updated_at = ? WHERE id = ?`, now-3600, older.ID); err != nil {
		t.Fatalf("age the older open issue: %v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET updated_at = ? WHERE id = ?`, now-60, newer.ID); err != nil {
		t.Fatalf("age the newer open issue: %v", err)
	}
	if _, err := database.Exec(`UPDATE issues SET updated_at = ?, last_recurrence_at = ? WHERE id = ?`, now, now, recurring.ID); err != nil {
		t.Fatalf("freshen the closed issue: %v", err)
	}

	issues, err := ListIssues(database, ListOptions{})
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	titles := make([]string, 0, len(issues))
	for _, issue := range issues {
		titles = append(titles, issue.Title)
	}
	want := []string{"newer open", "older open", "recurring closed"}
	if len(titles) != len(want) {
		t.Fatalf("listing = %v, want %v", titles, want)
	}
	for i := range want {
		if titles[i] != want[i] {
			t.Fatalf("listing = %v, want %v", titles, want)
		}
	}

	// --all must lead with open work too, not with the oldest closed history.
	all, err := ListIssues(database, ListOptions{IncludeClosed: true})
	if err != nil {
		t.Fatalf("ListIssues(all): %v", err)
	}
	if all[0].Status != "open" {
		t.Fatalf("--all led with a %s issue (%q); open work must come first",
			all[0].Status, all[0].Title)
	}
}
