package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Component is a thing issues are filed against: a tool, a service, a repo.
type Component struct {
	Name      string
	Prefix    string
	Repo      string // owner/name, for an explicit publish; may be empty
	Path      string // local checkout, so a session can resolve "." to a component
	CreatedAt int64
}

var (
	componentNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	prefixPattern        = regexp.MustCompile(`^[a-z]{1,5}$`)
	issueIDPattern       = regexp.MustCompile(`^([a-z]{1,5})([0-9]+)$`)
)

// DerivePrefix proposes a display prefix for a component name: the initials of
// a hyphenated name (agent-mail -> am), or the first two letters of a single
// word (labbook -> la). It is only a proposal; RegisterComponent takes an
// explicit prefix, which is how weft keeps "wb" and its existing citations.
func DerivePrefix(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '.' })
	if len(parts) > 1 {
		var b strings.Builder
		for _, part := range parts {
			if part == "" {
				continue
			}
			b.WriteByte(part[0])
			if b.Len() == 5 {
				break
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	letters := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, name)
	if len(letters) >= 2 {
		return letters[:2]
	}
	return letters
}

// RegisterComponent adds a component. An empty prefix is derived from the name;
// a prefix already in use is an error rather than a silent rename, because the
// prefix is how every recorded issue id resolves back to its component.
func RegisterComponent(database *sql.DB, c Component) (*Component, error) {
	c.Name = strings.ToLower(strings.TrimSpace(c.Name))
	if !componentNamePattern.MatchString(c.Name) {
		return nil, fmt.Errorf("invalid component name %q: use lowercase letters, digits, dot, dash, underscore", c.Name)
	}
	c.Prefix = strings.ToLower(strings.TrimSpace(c.Prefix))
	if c.Prefix == "" {
		c.Prefix = DerivePrefix(c.Name)
	}
	if !prefixPattern.MatchString(c.Prefix) {
		return nil, fmt.Errorf("invalid prefix %q: use 1-5 lowercase letters", c.Prefix)
	}
	c.Repo = strings.TrimSpace(c.Repo)
	c.Path = strings.TrimSpace(c.Path)
	c.CreatedAt = time.Now().Unix()

	if existing, err := GetComponent(database, c.Name); err == nil {
		return existing, fmt.Errorf("component %q already registered with prefix %q", existing.Name, existing.Prefix)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if owner, err := componentForPrefix(database, c.Prefix); err == nil {
		return nil, fmt.Errorf("prefix %q is already used by component %q; pass an explicit --prefix", c.Prefix, owner.Name)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	if _, err := database.Exec(
		`INSERT INTO components (name, prefix, repo, path, created_at) VALUES (?, ?, ?, ?, ?)`,
		c.Name, c.Prefix, c.Repo, c.Path, c.CreatedAt,
	); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateComponent sets the repo and/or local path. An empty value leaves the
// current one alone; the prefix is immutable once issues may cite it.
func UpdateComponent(database *sql.DB, name, repo, path string) (*Component, error) {
	component, err := GetComponent(database, name)
	if err != nil {
		return nil, err
	}
	if _, err := database.Exec(`
		UPDATE components
		   SET repo = CASE WHEN ? != '' THEN ? ELSE repo END,
		       path = CASE WHEN ? != '' THEN ? ELSE path END
		 WHERE name = ?`,
		repo, repo, path, path, component.Name,
	); err != nil {
		return nil, err
	}
	return GetComponent(database, component.Name)
}

func GetComponent(database *sql.DB, name string) (*Component, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	row := database.QueryRow(
		`SELECT name, prefix, repo, path, created_at FROM components WHERE name = ?`, name)
	return scanComponent(row)
}

func componentForPrefix(database *sql.DB, prefix string) (*Component, error) {
	row := database.QueryRow(
		`SELECT name, prefix, repo, path, created_at FROM components WHERE prefix = ?`, prefix)
	return scanComponent(row)
}

func scanComponent(row *sql.Row) (*Component, error) {
	var c Component
	if err := row.Scan(&c.Name, &c.Prefix, &c.Repo, &c.Path, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListComponents returns components alphabetically, with open and total counts.
func ListComponents(database *sql.DB) ([]ComponentSummary, error) {
	rows, err := database.Query(`
		SELECT c.name, c.prefix, c.repo, c.path, c.created_at,
		       COALESCE(SUM(CASE WHEN i.status = 'open' THEN 1 ELSE 0 END), 0) AS open_count,
		       COUNT(i.id) AS total_count
		  FROM components c
		  LEFT JOIN issues i ON i.component = c.name
		 GROUP BY c.name
		 ORDER BY c.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ComponentSummary
	for rows.Next() {
		var s ComponentSummary
		if err := rows.Scan(&s.Name, &s.Prefix, &s.Repo, &s.Path, &s.CreatedAt, &s.Open, &s.Total); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type ComponentSummary struct {
	Component
	Open  int
	Total int
}

// FormatID renders the citable id for an issue, for example wb139.
func FormatID(prefix string, number int64) string {
	return fmt.Sprintf("%s%d", prefix, number)
}

// ParseID splits a citable id into its prefix and number. It does not consult
// the ledger, so an unknown prefix parses here and fails at lookup.
func ParseID(s string) (prefix string, number int64, err error) {
	s = strings.ToLower(strings.TrimSpace(s))
	match := issueIDPattern.FindStringSubmatch(s)
	if match == nil {
		return "", 0, fmt.Errorf("invalid issue id %q: expected a form like wb139", s)
	}
	number, err = strconv.ParseInt(match[2], 10, 64)
	if err != nil || number <= 0 {
		return "", 0, fmt.Errorf("invalid issue id %q", s)
	}
	return match[1], number, nil
}
