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
}
