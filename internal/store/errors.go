package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Domain errors returned by the store (DESIGN.md §4.6). Check them with
// errors.Is.
var (
	ErrNotFound        = errors.New("resource not found")
	ErrVersionMismatch = errors.New("resource version does not match")
	ErrEmailTaken      = errors.New("email is already in use")
	ErrUnavailable     = errors.New("database temporarily unavailable")
)

// sqliteCode returns the extended result code of a SQLite error, or 0.
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

// wrap adds the operation name to an error that no operation-specific rule
// has classified, and applies the rules that hold for every operation: a
// busy database or an expired deadline is ErrUnavailable; a cancelled
// request stays context.Canceled.
//
// If the context has ended and err is one a context ending can cause (an
// interrupted statement, a transaction database/sql already rolled back, or
// a busy wait), the context error is added to the chain. Other errors, such
// as I/O errors or corruption, stay server errors even if the client has
// gone meanwhile. The current driver returns the context error itself in
// these cases, so this is a guard against drivers that report only their own.
func wrap(ctx context.Context, op string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil && !errors.Is(err, ctxErr) && causedByContext(err) {
		err = fmt.Errorf("%w: %w", ctxErr, err)
	}
	switch {
	case errors.Is(err, context.Canceled):
		// Checked first: a cancelled request is not a database problem, even
		// if the statement also reported SQLITE_BUSY.
		return fmt.Errorf("%s: %w", op, err)
	case errors.Is(err, context.DeadlineExceeded), sqliteCode(err)&0xff == sqlite3.SQLITE_BUSY:
		return fmt.Errorf("%s: %w: %w", op, ErrUnavailable, err)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// causedByContext reports whether err is one that a context ending can cause.
func causedByContext(err error) bool {
	code := sqliteCode(err) & 0xff
	return errors.Is(err, sql.ErrTxDone) || code == sqlite3.SQLITE_INTERRUPT || code == sqlite3.SQLITE_BUSY
}

// rollback rolls back tx when the function that began it returns. After a
// successful Commit, Rollback returns sql.ErrTxDone, which is expected; any
// other error is added to *err, because a failed rollback can leave the only
// connection inside a transaction.
func rollback(tx *sql.Tx, err *error) {
	if rerr := tx.Rollback(); rerr != nil && !errors.Is(rerr, sql.ErrTxDone) {
		*err = errors.Join(*err, fmt.Errorf("rollback: %w", rerr))
	}
}
