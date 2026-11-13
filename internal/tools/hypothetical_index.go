package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
	"github.com/8fclss/indexrx/internal/sqlguard"
)

const maxHypoIndexes = 5

const createHypoSQL = `
SELECT h.indexname, hypopg_relation_size(h.indexrelid)
FROM hypopg_create_index($1) h`

type HypotheticalIndexInput struct {
	Query   string   `json:"query" jsonschema:"a single SELECT statement to evaluate, with literal values"`
	Indexes []string `json:"indexes" jsonschema:"1 to 5 CREATE INDEX statements, e.g. CREATE INDEX ON orders (user_id, created_at)"`
}

type HypoIndexResult struct {
	Definition         string `json:"definition"`
	Name               string `json:"name" jsonschema:"generated name, as it appears in the plan"`
	EstimatedSizeBytes int64  `json:"estimated_size_bytes" jsonschema:"HypoPG estimate, usually 1.2-3.5x larger than a real btree index: treat as an upper bound"`
	UsedInPlan         bool   `json:"used_in_plan"`
}

type HypotheticalIndexOutput struct {
	Status     string            `json:"status" jsonschema:"estimated: costs compared with hypothetical indexes; unverifiable: an index type HypoPG cannot simulate"`
	Reason     string            `json:"reason,omitempty"`
	CostBefore float64           `json:"cost_before,omitempty"`
	CostAfter  float64           `json:"cost_after,omitempty"`
	CostRatio  float64           `json:"cost_ratio,omitempty" jsonschema:"cost_before / cost_after in planner units; not a measured speedup"`
	Indexes    []HypoIndexResult `json:"indexes,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
	PlanAfter  map[string]any    `json:"plan_after,omitempty"`
}

func (h *handlers) hypotheticalIndex(ctx context.Context, _ *mcp.CallToolRequest, in HypotheticalIndexInput) (*mcp.CallToolResult, HypotheticalIndexOutput, error) {
	if n := len(in.Indexes); n == 0 || n > maxHypoIndexes {
		return nil, HypotheticalIndexOutput{}, fmt.Errorf("provide 1 to %d index definitions, got %d", maxHypoIndexes, n)
	}
	if err := sqlguard.CheckSelect(in.Query); err != nil {
		return nil, HypotheticalIndexOutput{}, fmt.Errorf("query rejected: %w", err)
	}

	defs := make([]sqlguard.IndexDef, len(in.Indexes))
	var warnings []string
	for i, sql := range in.Indexes {
		def, err := sqlguard.CheckCreateIndex(sql)
		if errors.Is(err, sqlguard.ErrUnsupportedMethod) {
			return nil, HypotheticalIndexOutput{
				Status: "unverifiable",
				Reason: fmt.Sprintf("%q uses %s, which HypoPG cannot simulate; its benefit can only be checked by creating a real index on a test copy", sql, def.Method),
			}, nil
		}
		if err != nil {
			return nil, HypotheticalIndexOutput{}, fmt.Errorf("index %d rejected: %w", i+1, err)
		}
		if def.HasExpressions {
			warnings = append(warnings, fmt.Sprintf("%q indexes an expression: the planner has no statistics for it until a real index exists and ANALYZE runs, so row estimates may be far off", sql))
		}
		defs[i] = def
	}

	out := HypotheticalIndexOutput{Status: "estimated", Warnings: warnings}
	err := db.ReadOnlyWithCleanup(ctx, h.pool, "SELECT hypopg_reset()", func(tx pgx.Tx) error {
		for _, d := range defs {
			if _, err := findTable(ctx, tx, d.Schema, d.Table); err != nil {
				return err
			}
		}

		before, err := runExplain(ctx, tx, in.Query, false)
		if err != nil {
			return fmt.Errorf("explain before: %w", err)
		}

		for i, sql := range in.Indexes {
			r := HypoIndexResult{Definition: sql}
			if err := tx.QueryRow(ctx, createHypoSQL, sql).Scan(&r.Name, &r.EstimatedSizeBytes); err != nil {
				return fmt.Errorf("create hypothetical index %d: %w", i+1, err)
			}
			out.Indexes = append(out.Indexes, r)
		}

		after, err := runExplain(ctx, tx, in.Query, false)
		if err != nil {
			return fmt.Errorf("explain after: %w", err)
		}

		used := map[string]bool{}
		collectIndexNames(after.Root, used)
		for i := range out.Indexes {
			out.Indexes[i].UsedInPlan = used[out.Indexes[i].Name]
		}

		out.CostBefore = before.TotalCost
		out.CostAfter = after.TotalCost
		if after.TotalCost > 0 {
			out.CostRatio = before.TotalCost / after.TotalCost
		}
		out.PlanAfter = after.Full
		return nil
	})
	if err != nil {
		return nil, HypotheticalIndexOutput{}, err
	}
	return nil, out, nil
}

// collectIndexNames walks a JSON plan and records every "Index Name"
func collectIndexNames(node map[string]any, found map[string]bool) {
	if name, ok := node["Index Name"].(string); ok {
		found[name] = true
	}
	children, _ := node["Plans"].([]any)
	for _, c := range children {
		if m, ok := c.(map[string]any); ok {
			collectIndexNames(m, found)
		}
	}
}
