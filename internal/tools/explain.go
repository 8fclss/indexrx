package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
	"github.com/8fclss/indexrx/internal/sqlguard"
)

// maxPlanBytes caps what is sent back to the model.
// A compact plan format will replace the raw JSON later
const maxPlanBytes = 64 << 10

type ExplainInput struct {
	Query string `json:"query" jsonschema:"a single SELECT statement (WITH ... SELECT allowed), with literal values instead of $1 placeholders"`
}

type ExplainOutput struct {
	TotalCost float64        `json:"total_cost" jsonschema:"planner cost estimate in abstract units, not milliseconds"`
	PlanRows  float64        `json:"plan_rows" jsonschema:"estimated number of rows the query returns"`
	Plan      map[string]any `json:"plan" jsonschema:"plan tree from EXPLAIN (FORMAT JSON); estimates only, the query was not executed"`
}

func (h *handlers) explain(ctx context.Context, _ *mcp.CallToolRequest, in ExplainInput) (*mcp.CallToolResult, ExplainOutput, error) {
	if err := sqlguard.CheckSelect(in.Query); err != nil {
		return nil, ExplainOutput{}, fmt.Errorf("query rejected: %w", err)
	}

	var res planResult
	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		var err error
		res, err = runExplain(ctx, tx, in.Query, false)
		return err
	})
	if err != nil {
		return nil, ExplainOutput{}, err
	}
	return nil, ExplainOutput{TotalCost: res.TotalCost, PlanRows: res.PlanRows, Plan: res.Full}, nil
}

type planResult struct {
	TotalCost float64
	PlanRows  float64
	Root      map[string]any // plans[0]["Plan"]
	Full      map[string]any // plans[0]
}

// runExplain explains a query that already passed sqlguard.CheckSelect
func runExplain(ctx context.Context, tx pgx.Tx, query string, analyze bool) (planResult, error) {
	prefix, check := "EXPLAIN (FORMAT JSON, SETTINGS) ", sqlguard.CheckExplain
	if analyze {
		prefix, check = "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON, SETTINGS) ", sqlguard.CheckExplainAnalyze
	}
	stmt := prefix + strings.TrimRight(query, "; \t\r\n")
	if err := check(stmt); err != nil {
		return planResult{}, fmt.Errorf("internal check failed: %w", err)
	}

	var raw []byte
	if err := tx.QueryRow(ctx, stmt, pgx.QueryExecModeExec).Scan(&raw); err != nil {
		return planResult{}, err
	}
	if len(raw) > maxPlanBytes {
		return planResult{}, fmt.Errorf("plan is too large to return (%d KB); explain a smaller part of the query", len(raw)>>10)
	}

	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		return planResult{}, fmt.Errorf("decode plan: %w", err)
	}
	if len(plans) == 0 {
		return planResult{}, errors.New("empty plan")
	}
	root, _ := plans[0]["Plan"].(map[string]any)
	cost, _ := root["Total Cost"].(float64)
	rows, _ := root["Plan Rows"].(float64)
	return planResult{TotalCost: cost, PlanRows: rows, Root: root, Full: plans[0]}, nil
}
