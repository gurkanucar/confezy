package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"confezy/internal/model"
)

const flagColumns = `id, environment_id, key, enabled, description, version, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanFlag(s rowScanner) (model.Flag, error) {
	var f model.Flag
	err := s.Scan(&f.ID, &f.EnvironmentID, &f.Key, &f.Enabled, &f.Description, &f.Version, &f.UpdatedAt)
	return f, err
}

// ListFlags returns every flag in an environment, ordered by key.
func (d *DB) ListFlags(ctx context.Context, envID int64) ([]model.Flag, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT `+flagColumns+` FROM feature_flags WHERE environment_id = ? ORDER BY key ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("list flags: %w", err)
	}
	defer rows.Close()

	flags := []model.Flag{}
	for rows.Next() {
		f, err := scanFlag(rows)
		if err != nil {
			return nil, fmt.Errorf("list flags: %w", err)
		}
		flags = append(flags, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list flags: %w", err)
	}
	return flags, nil
}

// GetFlag returns a single flag.
func (d *DB) GetFlag(ctx context.Context, envID int64, key string) (model.Flag, error) {
	f, err := scanFlag(d.Read.QueryRowContext(ctx,
		`SELECT `+flagColumns+` FROM feature_flags WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flag{}, ErrNotFound
	}
	if err != nil {
		return model.Flag{}, fmt.Errorf("get flag: %w", err)
	}
	return f, nil
}

// CreateFlag inserts a flag at version 1 and bumps the environment timestamp.
// Returns ErrDuplicate if the key already exists in this environment.
func (d *DB) CreateFlag(ctx context.Context, envID int64, key string, enabled bool, description string) (model.Flag, error) {
	ts := now()

	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Flag{}, fmt.Errorf("create flag: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO feature_flags (environment_id, key, enabled, description, version, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?)`, envID, key, enabled, description, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return model.Flag{}, ErrDuplicate
		}
		return model.Flag{}, fmt.Errorf("create flag: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Flag{}, fmt.Errorf("create flag: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Flag{}, fmt.Errorf("create flag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Flag{}, fmt.Errorf("create flag: %w", err)
	}

	return model.Flag{
		ID: id, EnvironmentID: envID, Key: key, Enabled: enabled,
		Description: description, Version: 1, UpdatedAt: ts,
	}, nil
}

// UpdateFlag applies an optimistic-locking update. A nil description leaves the
// existing text alone. When expectedVersion does not match the stored version
// the current row is returned alongside ErrVersionConflict.
func (d *DB) UpdateFlag(ctx context.Context, envID int64, key string, enabled bool, description *string, expectedVersion int64) (model.Flag, error) {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Flag{}, fmt.Errorf("update flag: %w", err)
	}
	defer tx.Rollback()

	cur, err := scanFlag(tx.QueryRowContext(ctx,
		`SELECT `+flagColumns+` FROM feature_flags WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flag{}, ErrNotFound
	}
	if err != nil {
		return model.Flag{}, fmt.Errorf("update flag: %w", err)
	}
	if expectedVersion > 0 && cur.Version != expectedVersion {
		return cur, ErrVersionConflict
	}

	desc := cur.Description
	if description != nil {
		desc = *description
	}
	ts := now()

	_, err = tx.ExecContext(ctx,
		`UPDATE feature_flags SET enabled = ?, description = ?, version = version + 1, updated_at = ?
		   WHERE id = ?`, enabled, desc, ts, cur.ID)
	if err != nil {
		return model.Flag{}, fmt.Errorf("update flag: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Flag{}, fmt.Errorf("update flag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Flag{}, fmt.Errorf("update flag: %w", err)
	}

	cur.Enabled = enabled
	cur.Description = desc
	cur.Version++
	cur.UpdatedAt = ts
	return cur, nil
}

// DeleteFlag removes a flag, honouring expectedVersion when it is positive.
func (d *DB) DeleteFlag(ctx context.Context, envID int64, key string, expectedVersion int64) (model.Flag, error) {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Flag{}, fmt.Errorf("delete flag: %w", err)
	}
	defer tx.Rollback()

	cur, err := scanFlag(tx.QueryRowContext(ctx,
		`SELECT `+flagColumns+` FROM feature_flags WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Flag{}, ErrNotFound
	}
	if err != nil {
		return model.Flag{}, fmt.Errorf("delete flag: %w", err)
	}
	if expectedVersion > 0 && cur.Version != expectedVersion {
		return cur, ErrVersionConflict
	}

	ts := now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM feature_flags WHERE id = ?`, cur.ID); err != nil {
		return model.Flag{}, fmt.Errorf("delete flag: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Flag{}, fmt.Errorf("delete flag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Flag{}, fmt.Errorf("delete flag: %w", err)
	}
	return cur, nil
}
