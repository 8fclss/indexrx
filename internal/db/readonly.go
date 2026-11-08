package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ReadOnly runs fn inside a read-only transaction with server-side limits
// The transaction is always rolled back: tools never write, and ROLLBACK also undoes any SET that slipped through
func ReadOnly(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) (err error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() {
		// Roll back even if ctx is already cancelled
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("rollback: %w", rbErr)
		}
	}()

	// Server-owned constants: the agent never controls these strings
	for _, stmt := range []string{
		"SET LOCAL statement_timeout = '5s'",
		"SET LOCAL lock_timeout = '1s'",
	} {
		if _, err := tx.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("apply limit %q: %w", stmt, err)
		}
	}

	return fn(tx)
}
