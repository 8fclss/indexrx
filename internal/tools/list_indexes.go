package tools

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
)

const listIndexesSQL = `
SELECT i.relname,
       t.relname,
       pg_get_indexdef(i.oid),
       ix.indisunique,
       ix.indisprimary,
       ix.indisvalid,
       EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.oid),
       pg_relation_size(i.oid),
       COALESCE(s.idx_scan, 0),
       s.last_idx_scan
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = i.oid
WHERE n.nspname = $1
  AND ($2::oid IS NULL OR t.oid = $2)
  AND has_table_privilege(t.oid, 'SELECT')
ORDER BY t.relname, i.relname`

const statsSinceSQL = `
SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()`

type ListIndexesInput struct {
	Schema string `json:"schema,omitempty" jsonschema:"schema name, defaults to public"`
	Table  string `json:"table,omitempty" jsonschema:"table name; all tables of the schema if omitted"`
}

type IndexInfo struct {
	Name            string     `json:"name"`
	Table           string     `json:"table"`
	Definition      string     `json:"definition" jsonschema:"full CREATE INDEX statement, including WHERE for partial indexes"`
	IsUnique        bool       `json:"is_unique"`
	IsPrimary       bool       `json:"is_primary"`
	IsValid         bool       `json:"is_valid" jsonschema:"false if a CREATE INDEX CONCURRENTLY failed; the index is not used but still maintained"`
	BacksConstraint bool       `json:"backs_constraint" jsonschema:"index enforces a constraint (primary key, unique, exclusion) or is referenced by a foreign key; never drop it"`
	SizeBytes       int64      `json:"size_bytes"`
	Scans           int64      `json:"scans" jsonschema:"number of index scans since stats_since; also counts planner lookups, so a small number does not prove real use"`
	LastScan        *time.Time `json:"last_scan,omitempty"`
}

type ListIndexesOutput struct {
	Schema     string      `json:"schema"`
	StatsSince *time.Time  `json:"stats_since,omitempty" jsonschema:"when usage statistics were last reset; a short period makes scans unreliable"`
	Indexes    []IndexInfo `json:"indexes"`
}

func (h *handlers) listIndexes(ctx context.Context, _ *mcp.CallToolRequest, in ListIndexesInput) (*mcp.CallToolResult, ListIndexesOutput, error) {
	schema := schemaOrDefault(in.Schema)
	out := ListIndexesOutput{Schema: schema}

	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		if err := checkSchema(ctx, tx, schema); err != nil {
			return err
		}

		var tableOID *uint32
		if in.Table != "" {
			oid, err := findTable(ctx, tx, schema, in.Table)
			if err != nil {
				return err
			}
			tableOID = &oid
		}

		if err := tx.QueryRow(ctx, statsSinceSQL).Scan(&out.StatsSince); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, listIndexesSQL, schema, tableOID)
		if err != nil {
			return err
		}
		out.Indexes, err = pgx.CollectRows(rows, pgx.RowToStructByPos[IndexInfo])
		return err
	})
	if err != nil {
		return nil, ListIndexesOutput{}, err
	}
	if out.Indexes == nil {
		out.Indexes = []IndexInfo{}
	}
	return nil, out, nil
}
