package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Issue is one filed defect against one component.
type Issue struct {
	ID          int64 // internal surrogate key; not for display
	Component   string
	Prefix      string
	Number      int64 // per-component sequence; Prefix+Number is the citable id
	Status      string
	Title       string
	Kind        string
	Scope       string
	Likelihood  string
	Severity    string
	Fingerprint string
	Ref         string // occurrence identifier: a job id, host, commit, session
	Reporter    string // who filed it, usually a session name or project
	Summary     string
	Detail      string
	Occurrences int
	CreatedAt   int64
	UpdatedAt   int64
	ClosedAt    *int64
	CloseReason string
	// Recurrences counts reports of this fingerprint that arrived while the
	// issue was closed; a close verdict does not discard later observations.
	// Pre-close reports are counted in Occurrences.
	Recurrences int
	// LastRecurrenceAt is the most recent post-close report, 0 when none.
	LastRecurrenceAt int64
}

// CitableID is the form to quote in a notebook, commit message, or reply.
func (i Issue) CitableID() string { return FormatID(i.Prefix, i.Number) }

type Note struct {
	ID        int64
	IssueID   int64
	Body      string
	Author    string
	CreatedAt int64
}

// Report is a single observation. Fingerprint is what collapses repeat
// sightings onto one issue; when empty it falls back to the title.
type Report struct {
	Component   string
	Title       string
	Kind        string
	Scope       string
	Likelihood  string
	Severity    string
	Fingerprint string
	Ref         string
	Reporter    string
	Summary     string
	Detail      string
	Note        string
}

// FingerprintClosedError reports that a fingerprint belongs to a closed issue.
// The recurrence has already been recorded and committed when this is returned.
type FingerprintClosedError struct {
	IssueID     string
	Fingerprint string
	Recurrences int
	LastAt      int64
}

func (e *FingerprintClosedError) Error() string {
	recorded := ""
	if e.Recurrences > 0 {
		recorded = fmt.Sprintf("recurrence recorded (%d since close, most recent %s); ",
			e.Recurrences, time.Unix(e.LastAt, 0).Format("2006-01-02 15:04:05"))
	}
	return fmt.Sprintf("issue %s with fingerprint %q is closed; %sreopen it with `issues reopen %s`, or report with a different --fingerprint",
		e.IssueID, e.Fingerprint, recorded, e.IssueID)
}

// IsFingerprintClosed reports whether the ledger confirmed that a fingerprint
// belongs to a closed issue. A caller that files opportunistically can treat
// this as "already known and judged" rather than as a failure.
func IsFingerprintClosed(err error) bool {
	var target *FingerprintClosedError
	return errors.As(err, &target)
}

// UnknownComponentError distinguishes "you have not registered this component"
// from a malformed request, so a caller can offer to register it.
type UnknownComponentError struct{ Name string }

func (e *UnknownComponentError) Error() string {
	return fmt.Sprintf("unknown component %q: register it with `issues component add %s`", e.Name, e.Name)
}

// ReportIssue files an observation. It returns the affected issue and whether
// a new one was created. Three outcomes are possible:
//
//   - no issue with this (component, fingerprint): a new issue is opened.
//   - an OPEN issue matches: its occurrence count rises and any non-empty
//     fields in the report refresh the record. No duplicate is created.
//   - a CLOSED issue matches: the recurrence is recorded against it and
//     committed, then FingerprintClosedError is returned. Reporting never
//     reopens; the close was a verdict about the cause, and only a person or
//     agent looking at the evidence should overturn it.
func ReportIssue(database *sql.DB, report Report) (*Issue, bool, error) {
	if database == nil {
		return nil, false, errors.New("nil database")
	}
	normalizeReport(&report)
	if report.Component == "" {
		return nil, false, errors.New("component is required")
	}
	if report.Title == "" {
		return nil, false, errors.New("issue title is required")
	}
	if report.Fingerprint == "" {
		report.Fingerprint = report.Title
	}

	component, err := GetComponent(database, report.Component)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, &UnknownComponentError{Name: report.Component}
		}
		return nil, false, err
	}

	now := time.Now().Unix()
	tx, err := database.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(
		`SELECT id FROM issues WHERE status = 'open' AND component = ? AND fingerprint = ?`,
		component.Name, report.Fingerprint,
	).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}

	created := false
	if errors.Is(err, sql.ErrNoRows) {
		var closedID int64
		var closedNumber int64
		closedErr := tx.QueryRow(
			`SELECT id, number FROM issues
			  WHERE status = 'closed' AND component = ? AND fingerprint = ?
			  ORDER BY id LIMIT 1`,
			component.Name, report.Fingerprint,
		).Scan(&closedID, &closedNumber)
		if closedErr != nil && !errors.Is(closedErr, sql.ErrNoRows) {
			return nil, false, closedErr
		}
		if closedErr == nil {
			// The recurrence is recorded and committed before the error is
			// returned, so the record does not depend on how the caller
			// handles the error.
			if _, err := tx.Exec(`
				UPDATE issues
				   SET recurrences = recurrences + 1,
				       last_recurrence_at = ?,
				       updated_at = ?,
				       ref = CASE WHEN ref = '' THEN ? ELSE ref END
				 WHERE id = ?`,
				now, now, report.Ref, closedID,
			); err != nil {
				return nil, false, err
			}
			if report.Note != "" {
				if _, err := tx.Exec(
					`INSERT INTO issue_notes (issue_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
					closedID, report.Note, report.Reporter, now,
				); err != nil {
					return nil, false, err
				}
			}
			var recurrences int
			if err := tx.QueryRow(`SELECT recurrences FROM issues WHERE id = ?`, closedID).Scan(&recurrences); err != nil {
				return nil, false, err
			}
			if err := tx.Commit(); err != nil {
				return nil, false, err
			}
			return nil, false, &FingerprintClosedError{
				IssueID:     FormatID(component.Prefix, closedNumber),
				Fingerprint: report.Fingerprint,
				Recurrences: recurrences,
				LastAt:      now,
			}
		}

		// Per-component numbering, so each component's ids read as its own
		// sequence and an imported history keeps the numbers already cited
		// elsewhere. Safe under concurrency because the transaction took an
		// immediate write lock.
		var next int64
		if err := tx.QueryRow(
			`SELECT COALESCE(MAX(number), 0) + 1 FROM issues WHERE component = ?`, component.Name,
		).Scan(&next); err != nil {
			return nil, false, err
		}
		result, err := tx.Exec(`
			INSERT INTO issues
			    (component, number, status, title, kind, scope, likelihood, severity,
			     fingerprint, ref, reporter, summary, detail, occurrences, created_at, updated_at)
			VALUES (?, ?, 'open', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			component.Name, next, report.Title, report.Kind, report.Scope, report.Likelihood,
			report.Severity, report.Fingerprint, report.Ref, report.Reporter,
			report.Summary, report.Detail, now, now,
		)
		if err != nil {
			return nil, false, err
		}
		if id, err = result.LastInsertId(); err != nil {
			return nil, false, err
		}
		created = true
	} else {
		if _, err := tx.Exec(`
			UPDATE issues
			   SET occurrences = occurrences + 1,
			       updated_at = ?,
			       ref = CASE WHEN ? != '' THEN ? ELSE ref END,
			       reporter = CASE WHEN reporter = '' THEN ? ELSE reporter END,
			       summary = CASE WHEN ? != '' THEN ? ELSE summary END,
			       detail = CASE WHEN ? != '' THEN ? ELSE detail END
			 WHERE id = ?`,
			now,
			report.Ref, report.Ref,
			report.Reporter,
			report.Summary, report.Summary,
			report.Detail, report.Detail,
			id,
		); err != nil {
			return nil, false, err
		}
	}

	if report.Note != "" {
		if _, err := tx.Exec(
			`INSERT INTO issue_notes (issue_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
			id, report.Note, report.Reporter, now,
		); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	issue, err := getByRowID(database, id)
	return issue, created, err
}

// AddNote appends context to an existing issue. Notes are append-only: an
// issue's history is evidence, and evidence is not edited.
func AddNote(database *sql.DB, citableID, body, author string) (*Issue, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, errors.New("note body is required")
	}
	issue, err := GetIssue(database, citableID)
	if err != nil {
		return nil, err
	}
	if _, err := database.Exec(
		`INSERT INTO issue_notes (issue_id, body, author, created_at) VALUES (?, ?, ?, ?)`,
		issue.ID, body, strings.TrimSpace(author), time.Now().Unix(),
	); err != nil {
		return nil, err
	}
	return issue, nil
}

// CloseIssue records a verdict. Recurrence counters reset, so they always
// describe the interval since the current close: carrying them across would
// let an issue that recurred during an earlier closed interval resurface
// immediately after a later close, with nothing having happened since.
func CloseIssue(database *sql.DB, citableID, reason string) (*Issue, error) {
	issue, err := GetIssue(database, citableID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	result, err := database.Exec(`
		UPDATE issues
		   SET status = 'closed', closed_at = ?, close_reason = ?, updated_at = ?,
		       recurrences = 0, last_recurrence_at = 0
		 WHERE id = ? AND status = 'open'`,
		now, strings.TrimSpace(reason), now, issue.ID,
	)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return nil, fmt.Errorf("issue %s is already closed", issue.CitableID())
	}
	return getByRowID(database, issue.ID)
}

// ReopenIssue overturns a close. It refuses when another open issue in the
// same component already holds the fingerprint, because the partial unique
// index guarantees at most one, and silently merging two histories would lose
// one of them.
func ReopenIssue(database *sql.DB, citableID string) (*Issue, error) {
	issue, err := GetIssue(database, citableID)
	if err != nil {
		return nil, err
	}
	if issue.Status != "closed" {
		return nil, fmt.Errorf("issue %s is not closed", issue.CitableID())
	}
	if issue.Fingerprint != "" {
		var openNumber int64
		err := database.QueryRow(`
			SELECT number FROM issues
			 WHERE status = 'open' AND component = ? AND fingerprint = ? AND id != ?
			 LIMIT 1`,
			issue.Component, issue.Fingerprint, issue.ID,
		).Scan(&openNumber)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			return nil, fmt.Errorf("cannot reopen %s: open issue %s has the same fingerprint",
				issue.CitableID(), FormatID(issue.Prefix, openNumber))
		}
	}
	result, err := database.Exec(`
		UPDATE issues
		   SET status = 'open', closed_at = NULL, close_reason = '', updated_at = ?
		 WHERE id = ? AND status = 'closed'`,
		time.Now().Unix(), issue.ID,
	)
	if err != nil {
		return nil, err
	}
	if n, err := result.RowsAffected(); err == nil && n == 0 {
		return nil, sql.ErrNoRows
	}
	return getByRowID(database, issue.ID)
}

const issueColumns = `
	i.id, i.component, c.prefix, i.number, i.status, i.title, i.kind, i.scope,
	i.likelihood, i.severity, i.fingerprint, i.ref, i.reporter, i.summary, i.detail,
	i.occurrences, i.created_at, i.updated_at, i.closed_at, i.close_reason,
	i.recurrences, i.last_recurrence_at`

// GetIssue resolves a citable id such as wb139.
func GetIssue(database *sql.DB, citableID string) (*Issue, error) {
	prefix, number, err := ParseID(citableID)
	if err != nil {
		return nil, err
	}
	rows, err := database.Query(`
		SELECT `+issueColumns+`
		  FROM issues i JOIN components c ON c.name = i.component
		 WHERE c.prefix = ? AND i.number = ?`, prefix, number)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues, err := scanIssues(rows)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, fmt.Errorf("issue %s not found", strings.ToLower(citableID))
	}
	return &issues[0], nil
}

func getByRowID(database *sql.DB, rowID int64) (*Issue, error) {
	rows, err := database.Query(`
		SELECT `+issueColumns+`
		  FROM issues i JOIN components c ON c.name = i.component
		 WHERE i.id = ?`, rowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	issues, err := scanIssues(rows)
	if err != nil {
		return nil, err
	}
	if len(issues) == 0 {
		return nil, sql.ErrNoRows
	}
	return &issues[0], nil
}

// closedRecurrenceFloor is how far back a post-close recurrence stays visible
// in the default listing.
const closedRecurrenceFloor = 7 * 24 * time.Hour

// minRecurrencesToListClosed is how many post-close reports a closed issue
// needs before the default listing resurfaces it.
const minRecurrencesToListClosed = 2

// ListOptions filters a listing. Component empty means all components.
type ListOptions struct {
	Component     string
	IncludeClosed bool
}

// ListIssues lists open issues first, each group newest-activity first. By
// default it lists open issues plus
// closed issues whose fingerprint is still being observed: at least
// minRecurrencesToListClosed reports since close, the most recent within
// closedRecurrenceFloor. The count floor keeps a single straggler -- something
// that hit the fault just before the fix landed and reported after the close --
// from resurfacing a fixed issue, and the recency floor keeps the default list
// about ongoing problems rather than history. `issues show` and
// `issues list --all` always report the full recurrence count.
func ListIssues(database *sql.DB, opts ListOptions) ([]Issue, error) {
	query := `
		SELECT ` + issueColumns + `
		  FROM issues i JOIN components c ON c.name = i.component`
	var conds []string
	var args []any
	if component := strings.ToLower(strings.TrimSpace(opts.Component)); component != "" {
		conds = append(conds, `i.component = ?`)
		args = append(args, component)
	}
	if !opts.IncludeClosed {
		conds = append(conds, `(i.status = 'open' OR (i.recurrences >= ? AND i.last_recurrence_at >= ?))`)
		args = append(args, minRecurrencesToListClosed, time.Now().Add(-closedRecurrenceFloor).Unix())
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, ` AND `)
	}
	// Open work leads, then newest activity. Ordering by status alone would sort
	// 'closed' before 'open' lexicographically and bury the outstanding issues
	// under closed history, which is the opposite of what this listing is for.
	query += ` ORDER BY CASE WHEN i.status = 'open' THEN 0 ELSE 1 END, i.updated_at DESC, i.id DESC`

	rows, err := database.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIssues(rows)
}

func ListNotes(database *sql.DB, issueRowID int64) ([]Note, error) {
	rows, err := database.Query(`
		SELECT id, issue_id, body, author, created_at
		  FROM issue_notes WHERE issue_id = ?
		 ORDER BY created_at ASC, id ASC`, issueRowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notes []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.IssueID, &n.Body, &n.Author, &n.CreatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}

func normalizeReport(report *Report) {
	report.Component = strings.ToLower(strings.TrimSpace(report.Component))
	report.Title = strings.TrimSpace(report.Title)
	report.Kind = defaultTrim(report.Kind, "bug")
	report.Scope = strings.TrimSpace(report.Scope)
	report.Likelihood = defaultTrim(report.Likelihood, "unknown")
	report.Severity = defaultTrim(report.Severity, "notice")
	report.Fingerprint = strings.TrimSpace(report.Fingerprint)
	report.Ref = strings.TrimSpace(report.Ref)
	report.Reporter = strings.TrimSpace(report.Reporter)
	report.Summary = strings.TrimSpace(report.Summary)
	report.Detail = strings.TrimSpace(report.Detail)
	report.Note = strings.TrimSpace(report.Note)
}

func defaultTrim(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func scanIssues(rows *sql.Rows) ([]Issue, error) {
	var issues []Issue
	for rows.Next() {
		var issue Issue
		var closedAt sql.NullInt64
		if err := rows.Scan(
			&issue.ID, &issue.Component, &issue.Prefix, &issue.Number, &issue.Status,
			&issue.Title, &issue.Kind, &issue.Scope, &issue.Likelihood, &issue.Severity,
			&issue.Fingerprint, &issue.Ref, &issue.Reporter, &issue.Summary, &issue.Detail,
			&issue.Occurrences, &issue.CreatedAt, &issue.UpdatedAt, &closedAt,
			&issue.CloseReason, &issue.Recurrences, &issue.LastRecurrenceAt,
		); err != nil {
			return nil, err
		}
		if closedAt.Valid {
			issue.ClosedAt = &closedAt.Int64
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// Publication is a recorded export of an issue to a public tracker.
type Publication struct {
	IssueID     int64
	Repo        string
	URL         string
	PublishedAt int64
}

// RecordPublication notes that an issue was exported, so a second publish can
// point at the existing one instead of filing a duplicate.
func RecordPublication(database *sql.DB, issueRowID int64, repo, url string) error {
	_, err := database.Exec(`
		INSERT INTO issue_publications (issue_id, repo, url, published_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (issue_id, repo) DO UPDATE SET url = excluded.url, published_at = excluded.published_at`,
		issueRowID, strings.TrimSpace(repo), strings.TrimSpace(url), time.Now().Unix())
	return err
}

// GetPublication returns a prior export of an issue to a repo, if any.
func GetPublication(database *sql.DB, issueRowID int64, repo string) (*Publication, error) {
	var p Publication
	err := database.QueryRow(
		`SELECT issue_id, repo, url, published_at FROM issue_publications WHERE issue_id = ? AND repo = ?`,
		issueRowID, strings.TrimSpace(repo),
	).Scan(&p.IssueID, &p.Repo, &p.URL, &p.PublishedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}
