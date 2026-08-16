// Package db owns the SQLite connections, the migration runner and every
// query in the service. Reads go through a small pool, writes through a single
// connection so they serialise at the application level.
package db

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Sentinel errors returned by the query helpers. Handlers map these onto
// status codes.
var (
	ErrNotFound        = errors.New("not found")
	ErrDuplicate       = errors.New("already exists")
	ErrVersionConflict = errors.New("version conflict")
)

// pragmas is appended to both DSNs so every connection in both handles gets
// the same settings. modernc.org/sqlite applies these on connection open.
const pragmas = "_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&" +
	"_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"

// DB holds the read pool and the single-writer handle.
type DB struct {
	Read  *sql.DB
	Write *sql.DB
	Path  string

	// OnEnvChanged, when set, is called with an environment id after a change
	// beneath it has been committed. It is how webhook delivery is triggered
	// without this package knowing anything about webhooks or HTTP. It must not
	// block: the caller is still on the request path.
	OnEnvChanged func(envID int64)
}

// envChanged reports a committed change. Called after Commit, never before: a
// rolled-back transaction must not produce a notification.
func (d *DB) envChanged(envID int64) {
	if d.OnEnvChanged != nil {
		d.OnEnvChanged(envID)
	}
}

// Open opens (and creates if missing) the SQLite database at path.
func Open(dbPath string) (*DB, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("resolve db path: %w", err)
	}
	dsn := "file:" + abs + "?" + pragmas

	read, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read handle: %w", err)
	}
	read.SetMaxOpenConns(8)
	read.SetMaxIdleConns(8)
	read.SetConnMaxLifetime(time.Hour)

	write, err := sql.Open("sqlite", dsn)
	if err != nil {
		read.Close()
		return nil, fmt.Errorf("open write handle: %w", err)
	}
	// A single writer connection: SQLite allows only one writer anyway, and
	// serialising here avoids SQLITE_BUSY entirely.
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(time.Hour)

	d := &DB{Read: read, Write: write, Path: abs}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := write.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	if err := read.PingContext(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return d, nil
}

// Close closes both handles.
func (d *DB) Close() error {
	var first error
	if d.Read != nil {
		if err := d.Read.Close(); err != nil {
			first = err
		}
	}
	if d.Write != nil {
		if err := d.Write.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Migrate applies every embedded migration that has not run yet, in filename
// order, each inside its own transaction.
func (d *DB) Migrate(ctx context.Context) error {
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		name       TEXT PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`
	if _, err := d.Write.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(files)

	for _, file := range files {
		name := path.Base(file)

		var applied int
		err := d.Write.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE name = ?`, name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile(file)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := d.Write.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		for _, stmt := range splitStatements(string(content)) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, now())
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

// splitStatements breaks a migration file into individual statements. It
// understands single-quoted string literals and `--` line comments, which is
// everything the migrations in this repo use.
func splitStatements(src string) []string {
	var (
		out     []string
		cur     strings.Builder
		inStr   bool
		inLnCmt bool
	)
	runes := []rune(src)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case inLnCmt:
			if c == '\n' {
				inLnCmt = false
				cur.WriteRune(c)
			}
			continue
		case inStr:
			cur.WriteRune(c)
			if c == '\'' {
				// '' is an escaped quote inside a literal.
				if i+1 < len(runes) && runes[i+1] == '\'' {
					i++
					cur.WriteRune('\'')
				} else {
					inStr = false
				}
			}
			continue
		case c == '-' && i+1 < len(runes) && runes[i+1] == '-':
			inLnCmt = true
			i++
			continue
		case c == '\'':
			inStr = true
			cur.WriteRune(c)
			continue
		case c == ';':
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(c)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

// now returns the current unix timestamp, the only time representation stored.
func now() int64 { return time.Now().Unix() }

// touchEnv bumps environments.updated_at so client ETags change. Always called
// inside the same transaction as the change that caused it.
//
// MAX(now, updated_at + 1) rather than a plain assignment: the column has
// second resolution, so two writes inside the same second would otherwise leave
// the ETag unchanged and a polling client would keep getting 304 and never see
// the newer value. Forcing a strict increase costs at most a few seconds of
// apparent drift during a write burst, which nothing depends on — the column
// exists to be an opaque validator.
func touchEnv(ctx context.Context, tx *sql.Tx, envID, ts int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE environments SET updated_at = MAX(?, updated_at + 1) WHERE id = ?`, ts, envID)
	return err
}

// isUniqueViolation reports whether err is a UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
