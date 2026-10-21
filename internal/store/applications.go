package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

const applicationExists = `SELECT EXISTS (SELECT 1 FROM applications WHERE id = ?)`

const applicationColumns = "id, maintainer_id, title, version, company, website, source, license, description, " +
	"resource_version, created_at, updated_at"

func scanApplication(row scanner) (model.Application, error) {
	var a model.Application
	var created, updated string
	err := row.Scan(&a.ID, &a.MaintainerID, &a.Title, &a.Version, &a.Company, &a.Website, &a.Source,
		&a.License, &a.Description, &a.ResourceVersion, &created, &updated)
	if err != nil {
		return model.Application{}, err
	}
	a.CreatedAt, a.UpdatedAt, err = parseTimestamps("application", a.ID, created, updated)
	if err != nil {
		return model.Application{}, err
	}
	return a, nil
}

// CreateApplication stores a new Application from validated, normalized
// input. It returns ErrMaintainerNotFound if in.MaintainerID does not refer
// to an existing Maintainer; the foreign key decides (DESIGN.md §4.6).
func (s *Store) CreateApplication(ctx context.Context, in model.ApplicationInput) (model.Application, error) {
	id, err := newID()
	if err != nil {
		return model.Application{}, err
	}
	now := s.timestamp()
	a := model.Application{
		ID: id, MaintainerID: in.MaintainerID, Title: in.Title, Version: in.Version,
		Company: in.Company, Website: in.Website, Source: in.Source, License: in.License,
		Description: in.Description, ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO applications (`+applicationColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.MaintainerID, a.Title, a.Version, a.Company, a.Website, a.Source, a.License, a.Description,
		a.ResourceVersion, now.Format(timeLayout), now.Format(timeLayout))
	if err != nil {
		if sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
			return model.Application{}, fmt.Errorf("create application: %w: %w", ErrMaintainerNotFound, err)
		}
		return model.Application{}, wrap(ctx, "create application", err)
	}
	return a, nil
}

// GetApplication returns the Application with the given id, or ErrNotFound.
func (s *Store) GetApplication(ctx context.Context, id string) (model.Application, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+applicationColumns+` FROM applications WHERE id = ?`, id)
	a, err := scanApplication(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Application{}, fmt.Errorf("get application: %w", ErrNotFound)
	}
	if err != nil {
		return model.Application{}, wrap(ctx, "get application", err)
	}
	return a, nil
}

// ListApplications returns up to limit Applications after skipping offset,
// in creation order, and whether more exist. If maintainerID is not empty,
// only that Maintainer's Applications are listed. limit must be between 1
// and MaxPageSize and offset must not be negative.
func (s *Store) ListApplications(ctx context.Context, maintainerID string, limit, offset int) ([]model.Application, bool, error) {
	if limit < 1 || limit > MaxPageSize || offset < 0 {
		return nil, false, fmt.Errorf("list applications: invalid page limit %d offset %d", limit, offset)
	}
	// Two fixed statements rather than "? = '' OR maintainer_id = ?", so that
	// the filtered query can use idx_applications_maintainer.
	query := `SELECT ` + applicationColumns + ` FROM applications ORDER BY created_at, id LIMIT ? OFFSET ?`
	args := []any{limit + 1, offset}
	if maintainerID != "" {
		query = `SELECT ` + applicationColumns + ` FROM applications WHERE maintainer_id = ?
		          ORDER BY created_at, id LIMIT ? OFFSET ?`
		args = []any{maintainerID, limit + 1, offset}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, wrap(ctx, "list applications", err)
	}
	defer rows.Close()

	items := []model.Application{}
	for rows.Next() {
		a, err := scanApplication(rows)
		if err != nil {
			return nil, false, wrap(ctx, "list applications", err)
		}
		items = append(items, a)
	}
	if err := rows.Err(); err != nil {
		return nil, false, wrap(ctx, "list applications", err)
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

// UpdateApplication replaces the writable fields of the Application with the
// given id, if its resource version still equals version. maintainer_id is
// never written: an empty in.MaintainerID means it was omitted, and a given
// one must equal the stored value. It returns ErrNotFound,
// ErrVersionMismatch or ErrMaintainerChanged, in that order of precedence
// (DESIGN.md §4.2).
func (s *Store) UpdateApplication(ctx context.Context, id string, version int64, in model.ApplicationInput) (_ model.Application, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Application{}, wrap(ctx, "update application", err)
	}
	defer rollback(tx, &err)

	// The version and maintainerId checks are part of the UPDATE, so no
	// other write can come between the checks and the write.
	row := tx.QueryRowContext(ctx,
		`UPDATE applications
		    SET title = ?, version = ?, company = ?, website = ?, source = ?, license = ?, description = ?,
		        resource_version = resource_version + 1, updated_at = ?
		  WHERE id = ? AND resource_version = ? AND (? = '' OR maintainer_id = ?)
		RETURNING `+applicationColumns,
		in.Title, in.Version, in.Company, in.Website, in.Source, in.License, in.Description,
		s.timestamp().Format(timeLayout), id, version, in.MaintainerID, in.MaintainerID)
	a, err := scanApplication(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Application{}, fmt.Errorf("update application: %w", whyNoApplication(ctx, tx, id, version))
	case err != nil:
		return model.Application{}, wrap(ctx, "update application", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Application{}, wrap(ctx, "update application", err)
	}
	return a, nil
}

// whyNoApplication explains why a conditional update matched no row, reading
// in the same transaction: ErrNotFound, then ErrVersionMismatch, then
// ErrMaintainerChanged (the only condition left).
func whyNoApplication(ctx context.Context, tx *sql.Tx, id string, version int64) error {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT resource_version FROM applications WHERE id = ?`, id).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return wrap(ctx, "check application", err)
	case current != version:
		return ErrVersionMismatch
	default:
		return ErrMaintainerChanged
	}
}

// DeleteApplication deletes the Application with the given id, but only if
// its resource version still equals version. It returns ErrNotFound or
// ErrVersionMismatch.
func (s *Store) DeleteApplication(ctx context.Context, id string, version int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap(ctx, "delete application", err)
	}
	defer rollback(tx, &err)

	res, err := tx.ExecContext(ctx, `DELETE FROM applications WHERE id = ? AND resource_version = ?`, id, version)
	if err != nil {
		return wrap(ctx, "delete application", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrap(ctx, "delete application", err)
	}
	if n == 0 {
		return fmt.Errorf("delete application: %w", missingOrStale(ctx, tx, applicationExists, id))
	}
	if err := tx.Commit(); err != nil {
		return wrap(ctx, "delete application", err)
	}
	return nil
}
