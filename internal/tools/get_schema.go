package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
)

const findTableSQL = `
SELECT c.oid
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relname = $2
  AND c.relkind IN ('r', 'p')
  AND has_table_privilege(c.oid, 'SELECT')`

const columnsSQL = `
SELECT a.attname,
       format_type(a.atttypid, a.atttypmod),
       NOT a.attnotnull,
       pg_get_expr(d.adbin, d.adrelid)
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1
  AND a.attnum > 0
  AND NOT a.attisdropped
ORDER BY a.attnum`

const constraintsSQL = `
SELECT conname,
       CASE contype
         WHEN 'p' THEN 'primary_key'
         WHEN 'u' THEN 'unique'
         WHEN 'f' THEN 'foreign_key'
         WHEN 'c' THEN 'check'
         WHEN 'x' THEN 'exclusion'
         ELSE contype::text
       END,
       pg_get_constraintdef(oid)
FROM pg_constraint
WHERE conrelid = $1
ORDER BY conname`

type GetSchemaInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"schema name, defaults to public"`
	Table  string `json:"table" jsonschema:"table name"`
}

type Column struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Nullable bool    `json:"nullable"`
	Default  *string `json:"default,omitempty"`
}

type Constraint struct {
	Name       string `json:"name"`
	Kind       string `json:"kind" jsonschema:"primary_key, unique, foreign_key, check or exclusion"`
	Definition string `json:"definition"`
}

type GetSchemaOutput struct {
	Schema      string       `json:"schema"`
	Table       string       `json:"table"`
	Columns     []Column     `json:"columns"`
	Constraints []Constraint `json:"constraints"`
}

func (h *handlers) getSchema(ctx context.Context, _ *mcp.CallToolRequest, in GetSchemaInput) (*mcp.CallToolResult, GetSchemaOutput, error) {
	schema := schemaOrDefault(in.Schema)
	out := GetSchemaOutput{Schema: schema, Table: in.Table}

	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		var oid uint32
		if err := tx.QueryRow(ctx, findTableSQL, schema, in.Table).Scan(&oid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("table %s.%s not found or not readable", schema, in.Table)
			}
			return err
		}

		rows, err := tx.Query(ctx, columnsSQL, oid)
		if err != nil {
			return err
		}
		if out.Columns, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Column]); err != nil {
			return err
		}

		rows, err = tx.Query(ctx, constraintsSQL, oid)
		if err != nil {
			return err
		}
		out.Constraints, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Constraint])
		return err
	})
	if err != nil {
		return nil, GetSchemaOutput{}, err
	}
	if out.Constraints == nil {
		out.Constraints = []Constraint{}
	}
	return nil, out, nil
}
