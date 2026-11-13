package tools

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func Register(s *mcp.Server, pool *pgxpool.Pool) {
	h := &handlers{pool: pool, analyzeSlot: make(chan struct{}, 1)}

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

	mcp.AddTool(s, &mcp.Tool{
		Name:        "explain",
		Description: "Show the estimated execution plan of a SELECT query (EXPLAIN without ANALYZE: the query is NOT executed, numbers are planner estimates). Use it to see which scans and joins the planner chooses and how many rows it expects.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.explain)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "hypothetical_index",
		Description: "Estimate whether proposed indexes would help a SELECT without creating them (HypoPG). Compares planner cost before and after and reports whether the planner would use each index. Supports btree, hash and brin; GIN and GiST cannot be simulated. Results are planner estimates, not measured time. Check existing indexes with list_indexes first.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.hypotheticalIndex)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "explain_analyze",
		Description: "EXECUTE a SELECT query and return the actual plan with real row counts, timing and buffer usage (EXPLAIN ANALYZE, BUFFERS). Use it only when estimates from explain are not enough: it costs real time and load. Queries with a high estimated cost are not executed; queries slower than 5 s are cancelled and reported as timeout. Hypothetical indexes are ignored by it.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, h.explainAnalyze)
}
