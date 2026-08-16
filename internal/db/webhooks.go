package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"confezy/internal/model"
)

const webhookColumns = `id, environment_id, url, method, headers, label, enabled,
	created_at, last_status, last_error, last_fired_at`

func scanWebhook(s rowScanner) (model.Webhook, error) {
	var (
		w         model.Webhook
		headers   string
		status    sql.NullInt64
		firedAt   sql.NullInt64
		lastError sql.NullString
	)
	err := s.Scan(&w.ID, &w.EnvironmentID, &w.URL, &w.Method, &headers, &w.Label, &w.Enabled,
		&w.CreatedAt, &status, &lastError, &firedAt)
	if err != nil {
		return model.Webhook{}, err
	}

	w.Headers = map[string]string{}
	if headers != "" {
		// A malformed header blob must not take the whole listing down; the
		// webhook is still worth showing, just without its headers.
		_ = json.Unmarshal([]byte(headers), &w.Headers)
	}
	if status.Valid {
		v := status.Int64
		w.LastStatus = &v
	}
	if firedAt.Valid {
		v := firedAt.Int64
		w.LastFiredAt = &v
	}
	w.LastError = lastError.String
	return w, nil
}

// ListWebhooks returns every webhook on an environment, newest first.
func (d *DB) ListWebhooks(ctx context.Context, envID int64) ([]model.Webhook, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT `+webhookColumns+` FROM webhooks WHERE environment_id = ?
		 ORDER BY created_at DESC, id DESC`, envID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	hooks := []model.Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		hooks = append(hooks, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return hooks, nil
}

// ListEnabledWebhooks returns the webhooks that should actually be delivered.
func (d *DB) ListEnabledWebhooks(ctx context.Context, envID int64) ([]model.Webhook, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT `+webhookColumns+` FROM webhooks
		  WHERE environment_id = ? AND enabled = 1 ORDER BY id ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()

	hooks := []model.Webhook{}
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("list webhooks: %w", err)
		}
		hooks = append(hooks, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	return hooks, nil
}

// GetWebhook loads one webhook, scoped to its environment so a request cannot
// reach a webhook belonging elsewhere.
func (d *DB) GetWebhook(ctx context.Context, id, envID int64) (model.Webhook, error) {
	w, err := scanWebhook(d.Read.QueryRowContext(ctx,
		`SELECT `+webhookColumns+` FROM webhooks WHERE id = ? AND environment_id = ?`, id, envID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Webhook{}, ErrNotFound
	}
	if err != nil {
		return model.Webhook{}, fmt.Errorf("get webhook: %w", err)
	}
	return w, nil
}

// CreateWebhook adds a webhook to an environment.
func (d *DB) CreateWebhook(ctx context.Context, envID int64, url, method string, headers map[string]string, label string) (model.Webhook, error) {
	encoded, err := json.Marshal(headers)
	if err != nil {
		return model.Webhook{}, fmt.Errorf("create webhook: %w", err)
	}

	ts := now()
	res, err := d.Write.ExecContext(ctx,
		`INSERT INTO webhooks (environment_id, url, method, headers, label, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, 1, ?)`, envID, url, method, string(encoded), label, ts)
	if err != nil {
		return model.Webhook{}, fmt.Errorf("create webhook: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return model.Webhook{}, fmt.Errorf("create webhook: %w", err)
	}

	return model.Webhook{
		ID: id, EnvironmentID: envID, URL: url, Method: method,
		Headers: headers, Label: label, Enabled: true, CreatedAt: ts,
	}, nil
}

// SetWebhookEnabled turns delivery on or off without losing the configuration.
func (d *DB) SetWebhookEnabled(ctx context.Context, id, envID int64, enabled bool) error {
	res, err := d.Write.ExecContext(ctx,
		`UPDATE webhooks SET enabled = ? WHERE id = ? AND environment_id = ?`, enabled, id, envID)
	if err != nil {
		return fmt.Errorf("update webhook: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update webhook: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteWebhook removes a webhook.
func (d *DB) DeleteWebhook(ctx context.Context, id, envID int64) error {
	res, err := d.Write.ExecContext(ctx,
		`DELETE FROM webhooks WHERE id = ? AND environment_id = ?`, id, envID)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordWebhookResult stores the outcome of a delivery attempt. status is 0
// when the request never got a response.
func (d *DB) RecordWebhookResult(ctx context.Context, id int64, status int, deliveryErr string) error {
	var statusArg any
	if status > 0 {
		statusArg = status
	}
	_, err := d.Write.ExecContext(ctx,
		`UPDATE webhooks SET last_status = ?, last_error = ?, last_fired_at = ? WHERE id = ?`,
		statusArg, deliveryErr, now(), id)
	if err != nil {
		return fmt.Errorf("record webhook result: %w", err)
	}
	return nil
}
