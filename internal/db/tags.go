package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"confezy/internal/model"
)

// ListTags returns every tag defined on a project, ordered by name.
func (d *DB) ListTags(ctx context.Context, projectID int64) ([]model.Tag, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT id, project_id, name, created_at FROM tags WHERE project_id = ? ORDER BY name ASC`,
		projectID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	tags := []model.Tag{}
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
		tags = append(tags, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return tags, nil
}

// ListTagsForEnvironment returns the tags of the project the environment
// belongs to. The client API only knows its environment, so it resolves the
// project itself.
func (d *DB) ListTagsForEnvironment(ctx context.Context, envID int64) ([]model.Tag, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT t.id, t.project_id, t.name, t.created_at
		   FROM tags t
		   JOIN environments e ON e.project_id = t.project_id
		  WHERE e.id = ?
		  ORDER BY t.name ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	tags := []model.Tag{}
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("list tags: %w", err)
		}
		tags = append(tags, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	return tags, nil
}

// DeleteTag removes a tag from a project, detaching it from everything it was
// attached to. Environment stamps are bumped so filtered client responses stop
// showing it.
func (d *DB) DeleteTag(ctx context.Context, projectID int64, name string) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	defer tx.Rollback()

	var tagID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM tags WHERE project_id = ? AND name = ?`, projectID, name).Scan(&tagID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}

	if err := touchProjectEnvs(ctx, tx, projectID); err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	// The join rows go with it through ON DELETE CASCADE.
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE id = ?`, tagID); err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	return nil
}

// touchProjectEnvs bumps every environment of a project. Used when a change is
// project-wide, such as deleting a tag.
func touchProjectEnvs(ctx context.Context, tx *sql.Tx, projectID int64) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE environments SET updated_at = MAX(?, updated_at + 1) WHERE project_id = ?`,
		now(), projectID)
	return err
}

// tagsByOwner maps a flag or config id to its tag names.
type tagsByOwner map[int64][]string

// flagTags loads the tags of every flag in an environment in one query, so
// listing does not turn into one query per row.
func (d *DB) flagTags(ctx context.Context, envID int64) (tagsByOwner, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT ft.flag_id, t.name
		   FROM flag_tags ft
		   JOIN tags t ON t.id = ft.tag_id
		   JOIN feature_flags f ON f.id = ft.flag_id
		  WHERE f.environment_id = ?
		  ORDER BY t.name ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("load flag tags: %w", err)
	}
	defer rows.Close()

	out := tagsByOwner{}
	for rows.Next() {
		var (
			id   int64
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("load flag tags: %w", err)
		}
		out[id] = append(out[id], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load flag tags: %w", err)
	}
	return out, nil
}

// configTags is the config-side counterpart of flagTags.
func (d *DB) configTags(ctx context.Context, envID int64) (tagsByOwner, error) {
	rows, err := d.Read.QueryContext(ctx,
		`SELECT ct.config_id, t.name
		   FROM config_tags ct
		   JOIN tags t ON t.id = ct.tag_id
		   JOIN configs c ON c.id = ct.config_id
		  WHERE c.environment_id = ?
		  ORDER BY t.name ASC`, envID)
	if err != nil {
		return nil, fmt.Errorf("load config tags: %w", err)
	}
	defer rows.Close()

	out := tagsByOwner{}
	for rows.Next() {
		var (
			id   int64
			name string
		)
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("load config tags: %w", err)
		}
		out[id] = append(out[id], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load config tags: %w", err)
	}
	return out, nil
}

// TaggedFlag is a flag together with the tags attached to it.
type TaggedFlag struct {
	model.Flag
	Tags []string
}

// TaggedConfig is a config together with the tags attached to it.
type TaggedConfig struct {
	model.Config
	Tags []string
}

// ListFilter narrows a flag or config listing.
type ListFilter struct {
	// Tag keeps only rows carrying exactly this tag.
	Tag string
	// Query keeps rows whose key, or one of whose tags, contains this text
	// (case-insensitive substring).
	Query string
}

// Empty reports whether the filter would keep everything.
func (f ListFilter) Empty() bool { return f.Tag == "" && f.Query == "" }

// escapeLike neutralises the LIKE wildcards in user input. Without it a search
// for "new_checkout" would also match "newXcheckout", since _ is a wildcard.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// prefixed qualifies a comma-separated column list with a table alias.
func prefixed(columns, alias string) string {
	parts := strings.Split(columns, ", ")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}

// filterClauses builds the WHERE fragments shared by the flag and config
// listings. joinTable/joinKey name the link table for the owner kind.
func filterClauses(filter ListFilter, alias string, kind ownerKind) (string, []any) {
	var (
		clauses string
		args    []any
	)

	if filter.Tag != "" {
		// EXISTS rather than a JOIN: a JOIN would duplicate rows once the
		// query filter also has to look at tags.
		clauses += `
		  AND EXISTS (SELECT 1 FROM ` + kind.joinTable + ` jt
		                JOIN tags t ON t.id = jt.tag_id
		               WHERE jt.` + kind.joinKey + ` = ` + alias + `.id AND t.name = ?)`
		args = append(args, filter.Tag)
	}

	if filter.Query != "" {
		clauses += `
		  AND (` + alias + `.key LIKE ? ESCAPE '\'
		       OR EXISTS (SELECT 1 FROM ` + kind.joinTable + ` jq
		                    JOIN tags tq ON tq.id = jq.tag_id
		                   WHERE jq.` + kind.joinKey + ` = ` + alias + `.id
		                     AND tq.name LIKE ? ESCAPE '\'))`
		like := escapeLike(filter.Query)
		args = append(args, like, like)
	}

	return clauses, args
}

// ListFlagsTagged returns the flags of an environment with their tags, narrowed
// by filter.
func (d *DB) ListFlagsTagged(ctx context.Context, envID int64, filter ListFilter) ([]TaggedFlag, error) {
	where, filterArgs := filterClauses(filter, "f", ownerFlag)
	query := `SELECT ` + prefixed(flagColumns, "f") + `
		    FROM feature_flags f
		   WHERE f.environment_id = ?` + where + `
		   ORDER BY f.key ASC`
	args := append([]any{envID}, filterArgs...)

	rows, err := d.Read.QueryContext(ctx, query, args...)
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

	tags, err := d.flagTags(ctx, envID)
	if err != nil {
		return nil, err
	}

	out := make([]TaggedFlag, 0, len(flags))
	for _, f := range flags {
		out = append(out, TaggedFlag{Flag: f, Tags: tags[f.ID]})
	}
	return out, nil
}

// ListConfigsTagged is the config-side counterpart of ListFlagsTagged.
func (d *DB) ListConfigsTagged(ctx context.Context, envID int64, filter ListFilter) ([]TaggedConfig, error) {
	where, filterArgs := filterClauses(filter, "c", ownerConfig)
	query := `SELECT ` + prefixed(configColumns, "c") + `
		    FROM configs c
		   WHERE c.environment_id = ?` + where + `
		   ORDER BY c.key ASC`
	args := append([]any{envID}, filterArgs...)

	rows, err := d.Read.QueryContext(ctx, query, args...)
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

	tags, err := d.configTags(ctx, envID)
	if err != nil {
		return nil, err
	}

	out := make([]TaggedConfig, 0, len(configs))
	for _, c := range configs {
		out = append(out, TaggedConfig{Config: c, Tags: tags[c.ID]})
	}
	return out, nil
}

// AttachFlagTag links a tag to a flag, creating the tag on the project if it is
// new. Attaching is idempotent.
func (d *DB) AttachFlagTag(ctx context.Context, envID int64, key, tagName string) error {
	return d.attachTag(ctx, envID, key, tagName, ownerFlag)
}

// DetachFlagTag unlinks a tag from a flag. The tag itself stays on the project.
func (d *DB) DetachFlagTag(ctx context.Context, envID int64, key, tagName string) error {
	return d.detachTag(ctx, envID, key, tagName, ownerFlag)
}

// AttachConfigTag links a tag to a config, creating it if new.
func (d *DB) AttachConfigTag(ctx context.Context, envID int64, key, tagName string) error {
	return d.attachTag(ctx, envID, key, tagName, ownerConfig)
}

// DetachConfigTag unlinks a tag from a config.
func (d *DB) DetachConfigTag(ctx context.Context, envID int64, key, tagName string) error {
	return d.detachTag(ctx, envID, key, tagName, ownerConfig)
}

// ownerKind lets the attach/detach logic be written once for flags and configs,
// which differ only in the two table names involved.
type ownerKind struct {
	table     string // feature_flags / configs
	joinTable string // flag_tags / config_tags
	joinKey   string // flag_id / config_id
}

var (
	ownerFlag   = ownerKind{table: "feature_flags", joinTable: "flag_tags", joinKey: "flag_id"}
	ownerConfig = ownerKind{table: "configs", joinTable: "config_tags", joinKey: "config_id"}
)

func (d *DB) attachTag(ctx context.Context, envID int64, key, tagName string, kind ownerKind) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}
	defer tx.Rollback()

	var ownerID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM `+kind.table+` WHERE environment_id = ? AND key = ?`, envID, key).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}

	var projectID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT project_id FROM environments WHERE id = ?`, envID).Scan(&projectID); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}

	// Create the tag on first use; a second attach of the same name reuses it.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tags (project_id, name, created_at) VALUES (?, ?, ?)
		 ON CONFLICT (project_id, name) DO NOTHING`, projectID, tagName, now()); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}

	var tagID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM tags WHERE project_id = ? AND name = ?`, projectID, tagName).Scan(&tagID); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO `+kind.joinTable+` (`+kind.joinKey+`, tag_id) VALUES (?, ?)`,
		ownerID, tagID); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}

	// Tagging changes what ?tag= returns, so the validator has to move.
	if err := touchEnv(ctx, tx, envID, now()); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("attach tag: %w", err)
	}
	return nil
}

func (d *DB) detachTag(ctx context.Context, envID int64, key, tagName string, kind ownerKind) error {
	tx, err := d.Write.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	defer tx.Rollback()

	var ownerID int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM `+kind.table+` WHERE environment_id = ? AND key = ?`, envID, key).Scan(&ownerID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`DELETE FROM `+kind.joinTable+`
		  WHERE `+kind.joinKey+` = ?
		    AND tag_id IN (SELECT t.id FROM tags t
		                     JOIN environments e ON e.project_id = t.project_id
		                    WHERE e.id = ? AND t.name = ?)`, ownerID, envID, tagName)
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	if affected == 0 {
		return ErrNotFound
	}

	if err := touchEnv(ctx, tx, envID, now()); err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("detach tag: %w", err)
	}
	return nil
}
