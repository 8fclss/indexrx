package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/8fclss/indexrx/internal/db"
	"github.com/8fclss/indexrx/internal/sqlguard"
)

const (
	analyzeMaxCost = 1_000_000.0
	analyzeMaxRuns = 3
)

type ExplainAnalyzeInput struct {
	Query string `json:"query" jsonschema:"a single SELECT statement with literal values; it WILL be executed"`
	Runs  int    `json:"runs,omitempty" jsonschema:"how many times to execute, 1 to 3 (default 1); the first run is often slower because of a cold cache"`
}

type RunStats struct {
	ExecutionMs       float64 `json:"execution_ms"`
	PlanningMs        float64 `json:"planning_ms"`
	ActualRows        float64 `json:"actual_rows"`
	SharedHitBlocks   int64   `json:"shared_hit_blocks" jsonschema:"8 kB pages found in PostgreSQL's cache"`
	SharedReadBlocks  int64   `json:"shared_read_blocks" jsonschema:"8 kB pages not in PostgreSQL's cache (read from the OS cache or disk)"`
	TempWrittenBlocks int64   `json:"temp_written_blocks" jsonschema:"pages spilled to temporary files by sorts or hashes"`
}

type ExplainAnalyzeOutput struct {
	Status        string         `json:"status" jsonschema:"measured; too_expensive (estimated cost above the limit, not executed); timeout (did not finish within the statement timeout)"`
	Reason        string         `json:"reason,omitempty"`
	EstimatedCost float64        `json:"estimated_cost"`
	EstimatedRows float64        `json:"estimated_rows" jsonschema:"planner row estimate; compare with actual_rows to detect misestimates"`
	Runs          []RunStats     `json:"runs,omitempty"`
	Plan          map[string]any `json:"plan,omitempty" jsonschema:"plan of the last run with actual rows, loops, timing and buffers per node"`
}

func (h *handlers) explainAnalyze(ctx context.Context, _ *mcp.CallToolRequest, in ExplainAnalyzeInput) (*mcp.CallToolResult, ExplainAnalyzeOutput, error) {
	runs := in.Runs
	if runs == 0 {
		runs = 1
	}
	if runs < 1 || runs > analyzeMaxRuns {
		return nil, ExplainAnalyzeOutput{}, fmt.Errorf("runs must be between 1 and %d", analyzeMaxRuns)
	}
	if err := sqlguard.CheckSelect(in.Query); err != nil {
		return nil, ExplainAnalyzeOutput{}, fmt.Errorf("query rejected: %w", err)
	}

	select {
	case h.analyzeSlot <- struct{}{}:
		defer func() { <-h.analyzeSlot }()
	case <-ctx.Done():
		return nil, ExplainAnalyzeOutput{}, ctx.Err()
	}

	var out ExplainAnalyzeOutput
	err := db.ReadOnly(ctx, h.pool, func(tx pgx.Tx) error {
		est, err := runExplain(ctx, tx, in.Query, false)
		if err != nil {
			return fmt.Errorf("estimate: %w", err)
		}
		out.EstimatedCost, out.EstimatedRows = est.TotalCost, est.PlanRows
		if est.TotalCost > analyzeMaxCost {
			out.Status = "too_expensive"
			out.Reason = fmt.Sprintf("estimated cost %.0f is above the limit %.0f; the query was not executed. Use explain, or test a narrower version of the query", est.TotalCost, analyzeMaxCost)
			return nil
		}

		for i := 0; i < runs; i++ {
			res, err := runExplain(ctx, tx, in.Query, true)
			if err != nil {
				return fmt.Errorf("run %d: %w", i+1, err)
			}
			out.Runs = append(out.Runs, runStats(res))
			out.Plan = res.Full
		}
		out.Status = "measured"
		return nil
	})

	switch {
	case err == nil:
		return nil, out, nil
	case ctx.Err() != nil:
		return nil, ExplainAnalyzeOutput{}, ctx.Err() // the client cancelled
	case isQueryCanceled(err):
		return nil, ExplainAnalyzeOutput{
			Status:        "timeout",
			Reason:        "the query did not finish within the 5 s statement timeout, so it takes at least that long",
			EstimatedCost: out.EstimatedCost,
			EstimatedRows: out.EstimatedRows,
			Runs:          out.Runs,
		}, nil
	default:
		return nil, ExplainAnalyzeOutput{}, err
	}
}

// isQueryCanceled reports SQLSTATE 57014 (query_canceled), raised by statement_timeout
func isQueryCanceled(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "57014"
}

func runStats(r planResult) RunStats {
	num := func(m map[string]any, key string) float64 {
		v, _ := m[key].(float64)
		return v
	}
	return RunStats{
		ExecutionMs:       num(r.Full, "Execution Time"),
		PlanningMs:        num(r.Full, "Planning Time"),
		ActualRows:        num(r.Root, "Actual Rows"),
		SharedHitBlocks:   int64(num(r.Root, "Shared Hit Blocks")),
		SharedReadBlocks:  int64(num(r.Root, "Shared Read Blocks")),
		TempWrittenBlocks: int64(num(r.Root, "Temp Written Blocks")),
	}
}
