package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type txStarter interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// ReadOnly runs fn inside a read-only transaction on any pooled connection
func ReadOnly(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	return readOnlyTx(ctx, pool, fn)
}

// ReadOnlyWithCleanup runs fn in a read-only transaction on one dedicated
// connection, then runs cleanupSQL on that connection outside the transaction
func ReadOnlyWithCleanup(ctx context.Context, pool *pgxpool.Pool, cleanupSQL string, fn func(pgx.Tx) error) (err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if _, cerr := conn.Exec(cctx, cleanupSQL); cerr != nil {
			_ = conn.Conn().Close(cctx) // pool drops closed connections on Release
			if err == nil {
				err = fmt.Errorf("session cleanup: %w", cerr)
			}
		}
		conn.Release()
	}()

	return readOnlyTx(ctx, conn, fn)
}

func readOnlyTx(ctx context.Context, starter txStarter, fn func(pgx.Tx) error) (err error) {
	tx, err := starter.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin read-only transaction: %w", err)
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if rbErr := tx.Rollback(rbCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) && err == nil {
			err = fmt.Errorf("rollback: %w", rbErr)
		}
	}()

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
