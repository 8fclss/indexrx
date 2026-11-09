package tools

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
)

const listTablesSQL = `
SELECT c.relname,
       c.reltuples::bigint,
       pg_total_relation_size(c.oid)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1
  AND c.relkind IN ('r', 'p')
  AND has_table_privilege(c.oid, 'SELECT')
ORDER BY c.relname`

type ListTablesInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"schema name, defaults to public"`
}

type TableInfo struct {
	Name          string `json:"name"`
	EstimatedRows int64  `json:"estimated_rows" jsonschema:"planner estimate from statistics, -1 if the table was never analyzed"`
	TotalBytes    int64  `json:"total_bytes" jsonschema:"size including indexes and TOAST"`
}

type ListTablesOutput struct {
	Schema string      `json:"schema"`
	Tables []TableInfo `json:"tables"`
}

type handlers struct {
	pool *pgxpool.Pool
}

func (h *handlers) listTables(ctx context.Context, _ *mcp.CallToolRequest, in ListTablesInput) (*mcp.CallToolResult, ListTablesOutput, error) {
	schema := schemaOrDefault(in.Schema)

	var tables []TableInfo
	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		if err := checkSchema(ctx, tx, schema); err != nil {
			return err
		}

		rows, err := tx.Query(ctx, listTablesSQL, schema)
		if err != nil {
			return err
		}
		tables, err = pgx.CollectRows(rows, pgx.RowToStructByPos[TableInfo])
		return err
	})
	if err != nil {
		return nil, ListTablesOutput{}, err
	}
	if tables == nil {
		tables = []TableInfo{}
	}
	return nil, ListTablesOutput{Schema: schema, Tables: tables}, nil
}
