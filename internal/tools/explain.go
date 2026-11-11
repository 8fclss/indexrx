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
	TotalCost float64 `json:"total_cost" jsonschema:"planner cost estimate in abstract units, not milliseconds"`
	PlanRows  float64 `json:"plan_rows" jsonschema:"estimated number of rows the query returns"`
	Plan      any     `json:"plan" jsonschema:"plan tree from EXPLAIN (FORMAT JSON); estimates only, the query was not executed"`
}

func (h *handlers) explain(ctx context.Context, _ *mcp.CallToolRequest, in ExplainInput) (*mcp.CallToolResult, ExplainOutput, error) {
	if err := sqlguard.CheckSelect(in.Query); err != nil {
		return nil, ExplainOutput{}, fmt.Errorf("query rejected: %w", err)
	}

	query := strings.TrimRight(in.Query, "; \t\r\n")
	stmt := "EXPLAIN (FORMAT JSON, SETTINGS) " + query

	// Second check on the exact text that goes to the server
	if err := sqlguard.CheckExplain(stmt); err != nil {
		return nil, ExplainOutput{}, fmt.Errorf("internal check failed: %w", err)
	}

	var raw []byte
	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		// QueryExecModeExec: extended protocol (one statement only),
		// no cached prepared statement for one-off agent queries.
		return tx.QueryRow(ctx, stmt, pgx.QueryExecModeExec).Scan(&raw)
	})
	if err != nil {
		return nil, ExplainOutput{}, err
	}
	if len(raw) > maxPlanBytes {
		return nil, ExplainOutput{}, fmt.Errorf("plan is too large to return (%d KB); explain a smaller part of the query", len(raw)>>10)
	}

	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		return nil, ExplainOutput{}, fmt.Errorf("decode plan: %w", err)
	}
	if len(plans) == 0 {
		return nil, ExplainOutput{}, errors.New("empty plan")
	}
	root, _ := plans[0]["Plan"].(map[string]any)
	totalCost, _ := root["Total Cost"].(float64)
	planRows, _ := root["Plan Rows"].(float64)

	return nil, ExplainOutput{TotalCost: totalCost, PlanRows: planRows, Plan: plans[0]}, nil
}
