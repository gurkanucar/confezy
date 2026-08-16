package db

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// newTestDB opens a migrated database in a temporary directory.
func newTestDB(t *testing.T) *DB {
	t.Helper()

	database, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { database.Close() })

	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// queryPlan returns SQLite's plan for a statement, one line per step.
func queryPlan(t *testing.T, database *DB, sql string, args ...any) []string {
	t.Helper()

	rows, err := database.Read.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+sql, args...)
	if err != nil {
		t.Fatalf("explain: %v\nquery: %s", err, sql)
	}
	defer rows.Close()

	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("explain: %v", err)
	}
	return plan
}

// TestHotQueriesUseIndexes pins the access path of every query whose cost grows
// with the amount of data in an environment.
//
// This service adds no indexes of its own beyond the ones in the migrations,
// because it does not need any: the UNIQUE constraints already cover the way
// the data is read. `UNIQUE (environment_id, key)` on flags and configs is the
// index for both the single lookup and the ordered listing, `UNIQUE
// (project_id, name)` does the same for tags, and the join tables are reachable
// from either side through their primary key and the index on the other column.
//
// Measured on 20k flags, 20k configs and 40k tag links in one environment:
// the snapshot reads in 7ms, a single flag in under 0.01ms, and a tag-filtered
// listing in 7ms. Nothing here scans a table.
//
// That is a pleasant state of affairs and an easy one to lose — a column
// renamed, an ORDER BY changed, a UNIQUE dropped in a migration, and a query
// silently starts reading everything. Nothing would break; it would just get
// slower in proportion to how much the user had put in it, which is the kind of
// regression that reaches production. Hence this test.
func TestHotQueriesUseIndexes(t *testing.T) {
	database := newTestDB(t)
	const now = 1786880000

	cases := []struct {
		name string
		sql  string
		args []any
		// want is a fragment the plan must contain, naming the lookup this
		// query depends on. Matching the access rather than the index name
		// leaves room for a better index to be introduced later.
		want string
	}{
		{
			// Runs on every authenticated request in the service.
			name: "api key lookup",
			sql:  `SELECT id FROM api_keys WHERE key_hash = ? AND revoked_at IS NULL`,
			args: []any{"hash"},
			want: "key_hash=?",
		},
		{
			// The client API's hot path: everything a client polls for.
			name: "snapshot: flags",
			sql:  `SELECT id, key, enabled FROM feature_flags WHERE environment_id = ? ORDER BY key ASC`,
			args: []any{1},
			want: "environment_id=?",
		},
		{
			name: "snapshot: configs",
			sql:  `SELECT id, key, value FROM configs WHERE environment_id = ? ORDER BY key ASC`,
			args: []any{1},
			want: "environment_id=?",
		},
		{
			name: "single flag",
			sql:  `SELECT id FROM feature_flags WHERE environment_id = ? AND key = ?`,
			args: []any{1, "new_checkout"},
			want: "environment_id=? AND key=?",
		},
		{
			name: "single config",
			sql:  `SELECT id FROM configs WHERE environment_id = ? AND key = ?`,
			args: []any{1, "payment_rules"},
			want: "environment_id=? AND key=?",
		},
		{
			// The tag filter. The correlated subquery runs once per candidate
			// row, so it has to be a seek — a scan of the join table here would
			// make the whole listing quadratic.
			name: "flags filtered by tag",
			sql: `SELECT f.id FROM feature_flags f WHERE f.environment_id = ?
			        AND EXISTS (SELECT 1 FROM flag_tags jt JOIN tags t ON t.id = jt.tag_id
			                    WHERE jt.flag_id = f.id AND t.name = ?)
			      ORDER BY f.key ASC`,
			args: []any{1, "beta"},
			want: "flag_id=?",
		},
		{
			name: "configs filtered by tag",
			sql: `SELECT c.id FROM configs c WHERE c.environment_id = ?
			        AND EXISTS (SELECT 1 FROM config_tags ct JOIN tags t ON t.id = ct.tag_id
			                    WHERE ct.config_id = c.id AND t.name = ?)
			      ORDER BY c.key ASC`,
			args: []any{1, "beta"},
			want: "config_id=?",
		},
		{
			name: "tags in a project",
			sql:  `SELECT id, name FROM tags WHERE project_id = ? ORDER BY name ASC`,
			args: []any{1},
			want: "project_id=?",
		},
		{
			name: "resolve a tag by name",
			sql:  `SELECT id FROM tags WHERE project_id = ? AND name = ?`,
			args: []any{1, "beta"},
			want: "project_id=? AND name=?",
		},
		{
			// Grows with every login, so it is the one small table that is not
			// bounded by what a human typed.
			name: "session lookup",
			sql:  `SELECT id, user_id, expires_at FROM sessions WHERE id = ?`,
			args: []any{"sid"},
			want: "id=?",
		},
		{
			name: "prune expired sessions",
			sql:  `SELECT id FROM sessions WHERE expires_at < ?`,
			args: []any{now},
			want: "expires_at<?",
		},
		{
			name: "webhooks to deliver to",
			sql:  `SELECT id FROM webhooks WHERE environment_id = ? AND enabled = 1 ORDER BY id ASC`,
			args: []any{1},
			want: "environment_id=?",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := queryPlan(t, database, tc.sql, tc.args...)
			joined := strings.Join(plan, "\n  ")

			if !strings.Contains(joined, tc.want) {
				t.Errorf("plan does not use %q:\n  %s", tc.want, joined)
			}
			for _, step := range plan {
				if strings.HasPrefix(strings.TrimSpace(step), "SCAN") {
					t.Errorf("reads a whole table:\n  %s", joined)
					break
				}
			}
		})
	}
}
