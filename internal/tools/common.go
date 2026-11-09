package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const defaultSchema = "public"

const schemaAccessibleSQL = `
SELECT EXISTS (
  SELECT 1 FROM pg_namespace
  WHERE nspname = $1 AND has_schema_privilege(oid, 'USAGE')
)`

const findTableSQL = `
SELECT c.oid
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
  AND c.relkind IN ('r', 'p')
  AND has_schema_privilege(n.oid, 'USAGE')
  AND has_table_privilege(c.oid, 'SELECT')`

func schemaOrDefault(s string) string {
	if s == "" {
		return defaultSchema
	}
	return s
}

func checkSchema(ctx context.Context, tx pgx.Tx, schema string) error {
	var ok bool
	if err := tx.QueryRow(ctx, schemaAccessibleSQL, schema).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("schema %q not found or not accessible", schema)
	}
	return nil
}

func findTable(ctx context.Context, tx pgx.Tx, schema, table string) (uint32, error) {
	if table == "" {
		return 0, errors.New("table name is empty")
	}
	var oid uint32
	if err := tx.QueryRow(ctx, findTableSQL, schema, table).Scan(&oid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("table %s.%s not found or not readable", schema, table)
		}
		return 0, err
	}
	return oid, nil
}
