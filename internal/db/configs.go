package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"confezy/internal/model"
)

const configColumns = `id, environment_id, key, value, description, version, updated_at`

func scanConfig(s rowScanner) (model.Config, error) {
	var c model.Config
	err := s.Scan(&c.ID, &c.EnvironmentID, &c.Key, &c.Value, &c.Description, &c.Version, &c.UpdatedAt)
	return c, err
}

// ListConfigs returns every config in an environment, ordered by key.
func (d *DB) ListConfigs(ctx context.Context, envID int64) ([]model.Config, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT `+configColumns+` FROM configs WHERE environment_id = ? ORDER BY key ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("list configs: %w", err)
	}
	defer rows.Close()

	configs := []model.Config{}
	for rows.Next() {
		c, err := scanConfig(rows)
		if err != nil {
			return nil, fmt.Errorf("list configs: %w", err)
		}
		configs = append(configs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list configs: %w", err)
	}
	return configs, nil
}

// GetConfig returns a single config.
func (d *DB) GetConfig(ctx context.Context, envID int64, key string) (model.Config, error) {
	c, err := scanConfig(d.Read.QueryRowContext(ctx,
		`SELECT `+configColumns+` FROM configs WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Config{}, ErrNotFound
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("get config: %w", err)
	}
	return c, nil
}

// CreateConfig inserts a config at version 1. value must already be valid JSON;
// handlers check that before calling.
func (d *DB) CreateConfig(ctx context.Context, envID int64, key, value, description string) (model.Config, error) {
	ts := now()

	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Config{}, fmt.Errorf("create config: %w", err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO configs (environment_id, key, value, description, version, updated_at)
		 VALUES (?, ?, ?, ?, 1, ?)`, envID, key, value, description, ts)
	if err != nil {
		if isUniqueViolation(err) {
			return model.Config{}, ErrDuplicate
		}
		return model.Config{}, fmt.Errorf("create config: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Config{}, fmt.Errorf("create config: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Config{}, fmt.Errorf("create config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Config{}, fmt.Errorf("create config: %w", err)
	}

	return model.Config{
		ID: id, EnvironmentID: envID, Key: key, Value: value,
		Description: description, Version: 1, UpdatedAt: ts,
	}, nil
}

// UpdateConfig applies an optimistic-locking update. A nil description leaves
// the existing text alone. On a version mismatch the current row is returned
// alongside ErrVersionConflict.
func (d *DB) UpdateConfig(ctx context.Context, envID int64, key, value string, description *string, expectedVersion int64) (model.Config, error) {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Config{}, fmt.Errorf("update config: %w", err)
	}
	defer tx.Rollback()

	cur, err := scanConfig(tx.QueryRowContext(ctx,
		`SELECT `+configColumns+` FROM configs WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Config{}, ErrNotFound
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("update config: %w", err)
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
		`UPDATE configs SET value = ?, description = ?, version = version + 1, updated_at = ?
		   WHERE id = ?`, value, desc, ts, cur.ID)
	if err != nil {
		return model.Config{}, fmt.Errorf("update config: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Config{}, fmt.Errorf("update config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Config{}, fmt.Errorf("update config: %w", err)
	}

	cur.Value = value
	cur.Description = desc
	cur.Version++
	cur.UpdatedAt = ts
	return cur, nil
}

// DeleteConfig removes a config, honouring expectedVersion when positive.
func (d *DB) DeleteConfig(ctx context.Context, envID int64, key string, expectedVersion int64) (model.Config, error) {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return model.Config{}, fmt.Errorf("delete config: %w", err)
	}
	defer tx.Rollback()

	cur, err := scanConfig(tx.QueryRowContext(ctx,
		`SELECT `+configColumns+` FROM configs WHERE environment_id = ? AND key = ?`, envID, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Config{}, ErrNotFound
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("delete config: %w", err)
	}
	if expectedVersion > 0 && cur.Version != expectedVersion {
		return cur, ErrVersionConflict
	}

	ts := now()
	if _, err := tx.ExecContext(ctx, `DELETE FROM configs WHERE id = ?`, cur.ID); err != nil {
		return model.Config{}, fmt.Errorf("delete config: %w", err)
	}
	if err := touchEnv(ctx, tx, envID, ts); err != nil {
		return model.Config{}, fmt.Errorf("delete config: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Config{}, fmt.Errorf("delete config: %w", err)
	}
	return cur, nil
}
