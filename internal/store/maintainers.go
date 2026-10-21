package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	sqlite3 "modernc.org/sqlite/lib"

	"github.com/RayY-96/metadata-api-server/internal/model"
)

const maintainerColumns = "id, name, email, resource_version, created_at, updated_at"

func scanMaintainer(row scanner) (model.Maintainer, error) {
	var m model.Maintainer
	var created, updated string
	if err := row.Scan(&m.ID, &m.Name, &m.Email, &m.ResourceVersion, &created, &updated); err != nil {
		return model.Maintainer{}, err
	}
	var err error
	m.CreatedAt, m.UpdatedAt, err = parseTimestamps("maintainer", m.ID, created, updated)
	if err != nil {
		return model.Maintainer{}, err
	}
	return m, nil
}

// CreateMaintainer stores a new Maintainer from validated, normalized input.
// It returns ErrEmailTaken if the email is already in use.
func (s *Store) CreateMaintainer(ctx context.Context, in model.MaintainerInput) (model.Maintainer, error) {
	id, err := newID()
	if err != nil {
		return model.Maintainer{}, err
	}
	now := s.timestamp()
	m := model.Maintainer{
		ID: id, Name: in.Name, Email: in.Email,
		ResourceVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO maintainers (`+maintainerColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.Name, m.Email, m.ResourceVersion, now.Format(timeLayout), now.Format(timeLayout))
	if err != nil {
		if sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_UNIQUE {
			return model.Maintainer{}, fmt.Errorf("create maintainer: %w: %w", ErrEmailTaken, err)
		}
		return model.Maintainer{}, wrap(ctx, "create maintainer", err)
	}
	return m, nil
}

// GetMaintainer returns the Maintainer with the given id, or ErrNotFound.
func (s *Store) GetMaintainer(ctx context.Context, id string) (model.Maintainer, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+maintainerColumns+` FROM maintainers WHERE id = ?`, id)
	m, err := scanMaintainer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Maintainer{}, fmt.Errorf("get maintainer: %w", ErrNotFound)
	}
	if err != nil {
		return model.Maintainer{}, wrap(ctx, "get maintainer", err)
	}
	return m, nil
}

// ListMaintainers returns up to limit Maintainers after skipping offset, in
// creation order, and whether more exist. The limit is applied in SQL; one
// extra row is fetched to compute hasMore without counting (DESIGN.md §4.5).
// limit must be between 1 and MaxPageSize and offset must not be negative;
// the handler validates both.
func (s *Store) ListMaintainers(ctx context.Context, limit, offset int) ([]model.Maintainer, bool, error) {
	if limit < 1 || limit > MaxPageSize || offset < 0 {
		return nil, false, fmt.Errorf("list maintainers: invalid page limit %d offset %d", limit, offset)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+maintainerColumns+` FROM maintainers ORDER BY created_at, id LIMIT ? OFFSET ?`,
		limit+1, offset)
	if err != nil {
		return nil, false, wrap(ctx, "list maintainers", err)
	}
	defer rows.Close()

	items := []model.Maintainer{}
	for rows.Next() {
		m, err := scanMaintainer(rows)
		if err != nil {
			return nil, false, wrap(ctx, "list maintainers", err)
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, wrap(ctx, "list maintainers", err)
	}
	if len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

// UpdateMaintainer replaces the writable fields of the Maintainer with the
// given id, but only if its resource version still equals version
// (DESIGN.md §6.2). It returns ErrNotFound, ErrVersionMismatch or
// ErrEmailTaken.
func (s *Store) UpdateMaintainer(ctx context.Context, id string, version int64, in model.MaintainerInput) (_ model.Maintainer, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Maintainer{}, wrap(ctx, "update maintainer", err)
	}
	defer rollback(tx, &err)

	// The version check is part of the UPDATE, so no other write can come
	// between the check and the write.
	row := tx.QueryRowContext(ctx,
		`UPDATE maintainers
		    SET name = ?, email = ?, resource_version = resource_version + 1, updated_at = ?
		  WHERE id = ? AND resource_version = ?
		RETURNING `+maintainerColumns,
		in.Name, in.Email, s.timestamp().Format(timeLayout), id, version)
	m, err := scanMaintainer(row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No row matched: the Maintainer is missing or its version is stale.
		// Ask in the same transaction, so the answer refers to the same state.
		return model.Maintainer{}, fmt.Errorf("update maintainer: %w", missingOrStale(ctx, tx, maintainerExists, id))
	case sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_UNIQUE:
		return model.Maintainer{}, fmt.Errorf("update maintainer: %w: %w", ErrEmailTaken, err)
	case err != nil:
		return model.Maintainer{}, wrap(ctx, "update maintainer", err)
	}
	if err := tx.Commit(); err != nil {
		return model.Maintainer{}, wrap(ctx, "update maintainer", err)
	}
	return m, nil
}

// DeleteMaintainer deletes the Maintainer with the given id, but only if its
// resource version still equals version. It returns ErrNotFound,
// ErrVersionMismatch or, if Applications still refer to it,
// ErrMaintainerInUse. A stale version matches no row, so it is reported
// before the foreign key is checked (DESIGN.md §4.2).
func (s *Store) DeleteMaintainer(ctx context.Context, id string, version int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap(ctx, "delete maintainer", err)
	}
	defer rollback(tx, &err)

	res, err := tx.ExecContext(ctx, `DELETE FROM maintainers WHERE id = ? AND resource_version = ?`, id, version)
	if err != nil {
		if sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
			return fmt.Errorf("delete maintainer: %w: %w", ErrMaintainerInUse, err)
		}
		return wrap(ctx, "delete maintainer", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrap(ctx, "delete maintainer", err)
	}
	if n == 0 {
		return fmt.Errorf("delete maintainer: %w", missingOrStale(ctx, tx, maintainerExists, id))
	}
	if err := tx.Commit(); err != nil {
		return wrap(ctx, "delete maintainer", err)
	}
	return nil
}

const maintainerExists = `SELECT EXISTS (SELECT 1 FROM maintainers WHERE id = ?)`

// missingOrStale explains why a conditional write matched no row: it returns
// ErrNotFound if existsQuery (a constant EXISTS query on one table) finds no
// row with the id, else ErrVersionMismatch.
func missingOrStale(ctx context.Context, tx *sql.Tx, existsQuery, id string) error {
	var exists bool
	err := tx.QueryRowContext(ctx, existsQuery, id).Scan(&exists)
	switch {
	case err != nil:
		return wrap(ctx, "check resource exists", err)
	case !exists:
		return ErrNotFound
	default:
		return ErrVersionMismatch
	}
}
