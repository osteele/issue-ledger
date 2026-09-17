// Package store is the issue ledger: a single local SQLite database holding
// issues filed against any number of components.
//
// The ledger is deliberately shared rather than per-repository. An issue is
// almost always filed by an agent working in one project against a tool living
// in another, so a per-repository store would require the filer to locate and
// open a database it has no other business touching. One store keyed by
// component removes that coupling: the filer needs no access to the target
// repository at all.
//
// The ledger is local and is never synchronized. Issue text routinely carries
// job identifiers, unpublished research description, and absolute local paths.
// Publishing any of that is an explicit, per-issue decision (see cmd/publish),
// not a property of where the data lives.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// DBPathEnv overrides the ledger location, for tests and for keeping an
// experiment out of the real ledger.
const DBPathEnv = "AGENT_ISSUES_DB"

// DefaultPath returns the ledger location: $AGENT_ISSUES_DB when set,
// otherwise ~/.local/share/agent-issues/issues.db.
func DefaultPath() (string, error) {
	if override := os.Getenv(DBPathEnv); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "agent-issues", "issues.db"), nil
}

// Open opens the ledger at DefaultPath, creating and migrating it as needed.
func Open() (*sql.DB, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return OpenAt(path)
}

// OpenAt opens the ledger at an explicit path.
func OpenAt(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	// _txlock=immediate makes the read-then-write sequences in ReportIssue --
	// the open-fingerprint lookup and the per-component number allocation --
	// safe against a concurrent writer without an application-level lock.
	conn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(30000)&_pragma=foreign_keys(ON)&_txlock=immediate", path)
	database, err := sql.Open("sqlite", conn)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if err := initSchema(database); err != nil {
		database.Close()
		return nil, fmt.Errorf("init ledger schema: %w", err)
	}
	return database, nil
}

func initSchema(database *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS components (
			name TEXT PRIMARY KEY,
			prefix TEXT NOT NULL UNIQUE,
			repo TEXT NOT NULL DEFAULT '',
			path TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS issues (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			component TEXT NOT NULL REFERENCES components(name) ON DELETE RESTRICT,
			number INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'open',
			title TEXT NOT NULL,
			kind TEXT NOT NULL DEFAULT 'bug',
			scope TEXT NOT NULL DEFAULT '',
			likelihood TEXT NOT NULL DEFAULT 'unknown',
			severity TEXT NOT NULL DEFAULT 'notice',
			fingerprint TEXT NOT NULL DEFAULT '',
			ref TEXT NOT NULL DEFAULT '',
			reporter TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			detail TEXT NOT NULL DEFAULT '',
			occurrences INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			closed_at INTEGER,
			close_reason TEXT NOT NULL DEFAULT '',
			recurrences INTEGER NOT NULL DEFAULT 0,
			last_recurrence_at INTEGER NOT NULL DEFAULT 0,
			UNIQUE (component, number)
		)`,
		// Dedupe is per component: two tools may legitimately use the same
		// fingerprint string for unrelated faults. The partial unique index is
		// what makes "one open issue per fingerprint" a database invariant
		// rather than a convention the write path hopes to maintain.
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_open_fingerprint
			ON issues(component, fingerprint)
			WHERE status = 'open' AND fingerprint != ''`,
		`CREATE INDEX IF NOT EXISTS idx_issues_status_updated
			ON issues(status, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_issues_component_status
			ON issues(component, status, updated_at DESC)`,
		`CREATE TABLE IF NOT EXISTS issue_notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			body TEXT NOT NULL,
			author TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_issue_notes_issue_created
			ON issue_notes(issue_id, created_at ASC, id ASC)`,
		// Records which foreign ledger a row came from, so a re-run of an
		// import updates rather than duplicates.
		`CREATE TABLE IF NOT EXISTS issue_imports (
			source TEXT NOT NULL,
			legacy_id INTEGER NOT NULL,
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			imported_at INTEGER NOT NULL,
			PRIMARY KEY (source, legacy_id)
		)`,
		// Records a deliberate export to a public tracker.
		`CREATE TABLE IF NOT EXISTS issue_publications (
			issue_id INTEGER NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
			repo TEXT NOT NULL,
			url TEXT NOT NULL,
			published_at INTEGER NOT NULL,
			PRIMARY KEY (issue_id, repo)
		)`,
	}
	for _, stmt := range statements {
		if _, err := database.Exec(stmt); err != nil {
			return err
		}
	}
	return ensureColumns(database)
}

// columnDDLs maps each column that post-dates the original schema to its
// ALTER TABLE statement. The ledger carries no version counter, so schema
// evolution is an idempotent column check on every open; this is the same
// approach weft's bug ledger uses, and it tolerates an older binary having
// created the file.
func columnDDLs() [][3]string {
	return [][3]string{
		// {table, column, DDL}
	}
}

func ensureColumns(database *sql.DB) error {
	for _, col := range columnDDLs() {
		present, err := columnPresent(database, col[0], col[1])
		if err != nil {
			return err
		}
		if present {
			continue
		}
		if _, err := database.Exec(col[2]); err != nil {
			// A concurrent opener can add the column between the check and the
			// ALTER. The column existing now is success; anything else is real.
			if present, checkErr := columnPresent(database, col[0], col[1]); checkErr == nil && present {
				continue
			}
			return err
		}
	}
	return nil
}

func columnPresent(database *sql.DB, table, name string) (bool, error) {
	var count int
	err := database.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, name,
	).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
