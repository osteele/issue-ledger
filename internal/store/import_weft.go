package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WeftImportSource is the key recorded in issue_imports for rows that came
// from weft's own bug ledger.
const WeftImportSource = "weft-bugs.db"

// DefaultWeftBugDBPath is where weft keeps its standalone bug ledger: its XDG
// STATE directory, not its data directory. Note that a stray zero-byte
// bugs.db can exist under ~/.local/share/weft; it is not the ledger.
func DefaultWeftBugDBPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); override != "" && filepath.IsAbs(override) {
		return filepath.Join(override, "weft", "bugs.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "weft", "bugs.db"), nil
}

// WeftImportResult reports what an import did.
type WeftImportResult struct {
	Imported   int
	Skipped    int
	Reconciled int
	Notes      int
	MaxID      int64
}

// ImportWeftBugs copies weft's bug ledger into this one under the given
// component, preserving each bug's number so an id already cited in a lab
// notebook still resolves. It is idempotent: a bug already recorded in
// issue_imports is skipped rather than duplicated, so a re-run picks up only
// what is new.
//
// With reconcile set, a bug already imported has its status, close verdict and
// counters refreshed from the source, and any notes added since are appended.
// A migration is not instantaneous: the old ledger keeps receiving writes while
// the cutover happens, so a bug closed between the first import and the cutover
// would otherwise stay open here forever.
//
// The source is opened read-only. weft remains the owner of its own ledger
// until its tracker is pointed here; an import that mutated the source would
// make the two ledgers disagree about history.
func ImportWeftBugs(database *sql.DB, sourcePath, componentName string, reconcile bool) (*WeftImportResult, error) {
	component, err := GetComponent(database, componentName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &UnknownComponentError{Name: componentName}
		}
		return nil, err
	}
	if _, err := os.Stat(sourcePath); err != nil {
		return nil, fmt.Errorf("weft bug ledger at %s: %w", sourcePath, err)
	}

	legacy, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro", sourcePath))
	if err != nil {
		return nil, fmt.Errorf("open weft bug ledger: %w", err)
	}
	defer legacy.Close()

	hasRecurrence, err := legacyHasColumn(legacy, "bugs", "recurrences")
	if err != nil {
		return nil, err
	}
	recurrenceCols := "0 AS recurrences, 0 AS last_recurrence_at"
	if hasRecurrence {
		recurrenceCols = "recurrences, last_recurrence_at"
	}

	rows, err := legacy.Query(`
		SELECT id, status, title, kind, scope, likelihood, severity, fingerprint,
		       job_id, host, summary, detail, occurrences, created_at, updated_at,
		       closed_at, close_reason, ` + recurrenceCols + `
		  FROM bugs ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("read weft bugs: %w", err)
	}
	defer rows.Close()

	type legacyBug struct {
		ID                                               int64
		Status, Title, Kind, Scope, Likelihood, Severity string
		Fingerprint, Host, Summary, Detail, CloseReason  string
		JobID                                            sql.NullInt64
		Occurrences, Recurrences                         int
		CreatedAt, UpdatedAt, LastRecurrenceAt           int64
		ClosedAt                                         sql.NullInt64
	}
	var bugs []legacyBug
	for rows.Next() {
		var b legacyBug
		var host, summary, detail, closeReason sql.NullString
		if err := rows.Scan(
			&b.ID, &b.Status, &b.Title, &b.Kind, &b.Scope, &b.Likelihood, &b.Severity,
			&b.Fingerprint, &b.JobID, &host, &summary, &detail, &b.Occurrences,
			&b.CreatedAt, &b.UpdatedAt, &b.ClosedAt, &closeReason,
			&b.Recurrences, &b.LastRecurrenceAt,
		); err != nil {
			return nil, err
		}
		b.Host, b.Summary, b.Detail, b.CloseReason = host.String, summary.String, detail.String, closeReason.String
		bugs = append(bugs, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := &WeftImportResult{}
	now := time.Now().Unix()

	tx, err := database.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	for _, b := range bugs {
		var existing int64
		err := tx.QueryRow(
			`SELECT issue_id FROM issue_imports WHERE source = ? AND legacy_id = ?`,
			WeftImportSource, b.ID,
		).Scan(&existing)
		if err == nil {
			if !reconcile {
				result.Skipped++
				continue
			}
			if _, err := tx.Exec(`
				UPDATE issues
				   SET status = ?, closed_at = ?, close_reason = ?, occurrences = ?,
				       updated_at = ?, recurrences = ?, last_recurrence_at = ?,
				       summary = CASE WHEN ? != '' THEN ? ELSE summary END,
				       detail = CASE WHEN ? != '' THEN ? ELSE detail END
				 WHERE id = ?`,
				b.Status, nullableInt64(b.ClosedAt), b.CloseReason, b.Occurrences,
				b.UpdatedAt, b.Recurrences, b.LastRecurrenceAt,
				b.Summary, b.Summary, b.Detail, b.Detail, existing,
			); err != nil {
				return nil, fmt.Errorf("reconcile weft bug %d: %w", b.ID, err)
			}
			added, err := copyWeftNotes(tx, legacy, b.ID, existing)
			if err != nil {
				return nil, err
			}
			result.Notes += added
			result.Reconciled++
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		// A number already taken in this component means the ledger was
		// written to directly before the import. Refusing is right: silently
		// renumbering would break the very citations the import exists to keep.
		var clash int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM issues WHERE component = ? AND number = ?`, component.Name, b.ID,
		).Scan(&clash); err != nil {
			return nil, err
		}
		if clash > 0 {
			return nil, fmt.Errorf("cannot import weft bug %d: component %q already has issue %s",
				b.ID, component.Name, FormatID(component.Prefix, b.ID))
		}

		insert, err := tx.Exec(`
			INSERT INTO issues
			    (component, number, status, title, kind, scope, likelihood, severity,
			     fingerprint, ref, reporter, summary, detail, occurrences,
			     created_at, updated_at, closed_at, close_reason,
			     recurrences, last_recurrence_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			component.Name, b.ID, b.Status, b.Title, b.Kind, b.Scope, b.Likelihood, b.Severity,
			b.Fingerprint, weftRef(b.JobID, b.Host), b.Summary, b.Detail, b.Occurrences,
			b.CreatedAt, b.UpdatedAt, nullableInt64(b.ClosedAt), b.CloseReason,
			b.Recurrences, b.LastRecurrenceAt,
		)
		if err != nil {
			return nil, fmt.Errorf("import weft bug %d: %w", b.ID, err)
		}
		issueID, err := insert.LastInsertId()
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(
			`INSERT INTO issue_imports (source, legacy_id, issue_id, imported_at) VALUES (?, ?, ?, ?)`,
			WeftImportSource, b.ID, issueID, now,
		); err != nil {
			return nil, err
		}

		noteCount, err := copyWeftNotes(tx, legacy, b.ID, issueID)
		if err != nil {
			return nil, err
		}
		result.Notes += noteCount
		result.Imported++
		if b.ID > result.MaxID {
			result.MaxID = b.ID
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func copyWeftNotes(tx *sql.Tx, legacy *sql.DB, legacyBugID, issueID int64) (int, error) {
	rows, err := legacy.Query(
		`SELECT body, created_at FROM bug_notes WHERE bug_id = ? ORDER BY created_at ASC, id ASC`,
		legacyBugID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var body string
		var createdAt int64
		if err := rows.Scan(&body, &createdAt); err != nil {
			return count, err
		}
		// Identity is (issue, body, timestamp): re-running the import must not
		// append a second copy of a note it already carried over.
		var present int
		if err := tx.QueryRow(
			`SELECT COUNT(*) FROM issue_notes WHERE issue_id = ? AND body = ? AND created_at = ?`,
			issueID, body, createdAt,
		).Scan(&present); err != nil {
			return count, err
		}
		if present > 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO issue_notes (issue_id, body, author, created_at) VALUES (?, ?, 'weft', ?)`,
			issueID, body, createdAt,
		); err != nil {
			return count, err
		}
		count++
	}
	return count, rows.Err()
}

// weftRef folds weft's job_id and host into the generic ref field, keeping the
// job id in the form it is cited as elsewhere.
func weftRef(jobID sql.NullInt64, host string) string {
	var parts []string
	if jobID.Valid {
		parts = append(parts, fmt.Sprintf("wj%d", jobID.Int64))
	}
	if host = strings.TrimSpace(host); host != "" {
		parts = append(parts, host)
	}
	return strings.Join(parts, " on ")
}

func nullableInt64(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

func legacyHasColumn(database *sql.DB, table, column string) (bool, error) {
	var count int
	err := database.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
