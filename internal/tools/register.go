package tools

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Register(s *mcp.Server, pool *pgxpool.Pool) {
	h := &handlers{pool: pool}

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_tables",
		Description: "List tables in a schema with estimated row counts and total size. Call it first to see what the database contains.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.listTables)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_schema",
		Description: "Get columns (with types, nullability, defaults) and constraints of one table. Call it before reasoning about indexes or rewriting a query on that table.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.getSchema)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_indexes",
		Description: "List existing indexes with full definitions, size, constraint flags and usage statistics. Call it before proposing a new index, to avoid duplicates, and when looking for redundant or unused indexes.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.listIndexes)
}
