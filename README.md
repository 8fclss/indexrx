# indexrx

An AI agent that diagnoses slow PostgreSQL queries and proposes fixes backed by measurements, not guesses: an index when an index helps, `ANALYZE` when statistics are stale, extended statistics for correlated columns, dropping redundant indexes, or "do nothing" when the plan is already right.

The agent will work through an MCP server written in Go (`EXPLAIN`, schema, statistics, hypothetical indexes via HypoPG) under a read-only database role. This repository currently contains the groundwork (a reproducible PostgreSQL test stand, a hand-measured [eval set](eval/) of 12 cases with reference answers and a [security review](docs/agent-role-security.md) of the agent role) and the MCP server with its six tools. The agent itself is the next stage.

## Status

| Stage | What | State |
|-------|------|-------|
| 1 | PostgreSQL 17 in Docker with HypoPG, `pg_stat_statements`, `pg_trgm`; skewed, reproducible seed data | done |
| 2 | Eval set: 12 slow-query cases measured by hand with `EXPLAIN (ANALYZE, BUFFERS)`, reference answers in [`eval/expected.yml`](eval/expected.yml) | done |
| 3 | Read-only agent role and 38 attempts to break it, requirements for the server ([`docs/agent-role-security.md`](docs/agent-role-security.md)) | done |
| 4 | MCP server in Go: `list_tables`, `get_schema`, `list_indexes`, `explain`, `explain_analyze`, `hypothetical_index`; SQL checked on the PostgreSQL parse tree. Open: [security tasks](docs/agent-role-security.md#open-tasks), compact plan format, statistics tools | in progress |
| 5 | Agent in Python, run against the eval set | planned |

## Quick start

Requirements: Docker with Compose, a bash shell for the scripts (Linux, macOS, WSL or Git Bash), Go 1.25+ with cgo and a C compiler for the MCP server (the SQL parser [pg_query_go](https://github.com/pganalyze/pg_query_go) is a C library), Node.js for MCP Inspector.

```bash
cp .env.example .env              # set the passwords
docker compose up -d --build      # PostgreSQL 17 + HypoPG, roles from db/init
./scripts/seed.sh                 # ~9M rows, takes a few minutes
./scripts/explain.sh eval/cases/01_composite_order.sql
```

`seed.sh` uses `setseed`, so every run produces the same data. Dates are relative to `now()`.

To start from scratch (for example after a change in `db/init`, which only runs on an empty volume):

```bash
docker compose down -v && docker compose up -d --build && ./scripts/seed.sh
```

## Test data

| Table | Rows | Size | Notes |
|-------|------|------|-------|
| `users` | 200k | 15 MB | `country` (10) and `city` (100), the city determines the country |
| `orders` | 2M | 145 MB | `user_id` skewed with `random()^3`: user 1 has ~34k orders, most users 2–3; 0.5% `pending` |
| `order_items` | 4M | 230 MB | ~2 items per order |
| `events` | 3M | 341 MB | `payload jsonb` with `device`, `page`, `ab_test` |

Only primary keys exist after seeding; every case creates and drops its own indexes.

## Measurement setup

All measurements in [`eval/`](eval/) were taken on one machine:

| | |
|---|---|
| CPU | Intel Core i7-14700KF (20 cores / 28 threads) |
| RAM | 64 GB |
| Storage | NVMe SSD |
| Host | Windows, Docker Desktop with the WSL2 backend, default WSL2 limits (all logical CPUs, half of the RAM) |
| PostgreSQL | 17 from the official `postgres:17` image + HypoPG |

Buffers and plans do not depend on the hardware, times do. Cases 01–06 were measured before `users.city` was added and before seeding became deterministic, so their exact row counts differ slightly from a fresh seed.

## Eval set

Each case is a query (or a task) with a known correct answer type. Measured on the stand above; buffers are `shared hit + read`, times are warm runs.

| # | Problem | Answer type | Measured effect |
|---|---------|-------------|-----------------|
| 01 | Filter + `ORDER BY` + `LIMIT` on one user | `create_index` | composite `(user_id, created_at DESC)`: 18624 → 26 buffers |
| 02 | Rare status (0.5% of rows) | `create_index` | partial covering index: 18510 → 52 buffers, 416 kB |
| 03 | `lower(email) = ...` | `create_index` | expression index + `ANALYZE`: 1860 → 4 buffers |
| 04 | `LIKE '%...%'` | `create_index` | GIN `gin_trgm_ops`: 1860 → 206 buffers |
| 05 | `jsonb @>` | `create_index` | GIN: 43603 → 3732 buffers (`jsonb_path_ops`: 2942) |
| 06 | Join without indexes | `create_index` | both join-side indexes: 48118 → 5966 buffers, 140 → 3.9 ms |
| 07 | Correlated columns | `extended_statistics` | `CREATE STATISTICS`: estimate 217 → 2160 rows (actual 2006) |
| 08 | Stale statistics after a bulk load | `analyze` | `ANALYZE`: estimate 7893 → 398931 rows (actual 399756) |
| 09 | Duplicate and redundant indexes | `drop_index` | drop 2 of 5: −35 MB, insert of 100k rows 372 → 237 ms |
| 10 | Unused index | `drop_index_with_caveats` | `idx_scan = 0` over seconds of statistics proves little |
| 11 | Seq Scan is the right plan | `no_action` | an index is ignored by the planner; proposing one is a false positive |
| 12 | Sort spills to disk | `work_mem` | in-memory sort needs ~124 MB, and the plan loses parallelism |

- [`eval/cases/`](eval/cases/) — the queries and task descriptions, [`eval/setup/`](eval/setup/) — set-up and tear-down for cases 08–10.
- [`eval/results.md`](eval/results.md) — full measurements, rejected variants and what the hypotheses got wrong.
- [`eval/expected.yml`](eval/expected.yml) — machine-readable reference answers: acceptable, best, suboptimal and unacceptable solutions, required diagnosis and caveats, false-positive rules.
- [`eval/hypopg.md`](eval/hypopg.md) — which of the solutions can be verified with hypothetical indexes (B-tree yes, GIN no) and how far HypoPG's costs and sizes are from real ones.

Some cases are traps for an agent that always proposes an index: in 08 the fix is `ANALYZE`, in 11 nothing, in 12 an index on `total` is chosen by the planner and makes the query twice as slow.

## MCP server

`cmd/indexrx-mcp` is an MCP server over stdio, built with the official [Go SDK](https://github.com/modelcontextprotocol/go-sdk) and [pgx](https://github.com/jackc/pgx). It connects as `indexrx_agent` and implements the requirements of the [security review](docs/agent-role-security.md#implementation-status-of-the-server-requirements):

- every tool call runs in its own `BEGIN READ ONLY` transaction with `SET LOCAL statement_timeout = '5s'` and `lock_timeout = '1s'` set by the server, and the transaction is always rolled back ([`internal/db/readonly.go`](internal/db/readonly.go));
- the catalog tools are fixed SQL with bind parameters; schema and table names are checked against the role's actual rights, so a missing table and a table the agent cannot read give the same error;
- the only SQL the agent writes is the query being analyzed. [`internal/sqlguard`](internal/sqlguard/guard.go) parses it with the PostgreSQL parser and accepts exactly one `SELECT` without data-modifying CTEs, row locks, `SELECT INTO`, placeholders or functions with side effects (`set_config`, `pg_sleep`, advisory locks, `pg_terminate_backend`, `hypopg_*`, ...). The final `EXPLAIN` text is checked again before it is sent, over the extended protocol, so a second statement cannot slip through;
- `explain_analyze` runs only below an estimated cost of 1,000,000, one call at a time, at most 3 runs of 5 s each; temporary files are capped by the role's `temp_file_limit`;
- `hypothetical_index` works on a dedicated connection and calls `hypopg_reset()` after every call, also after errors;
- the pool is capped at 4 connections, one below the role's `CONNECTION LIMIT 5`; stdout is the protocol channel, logs go to stderr.

Tools:

| Tool | Input | What it returns |
|------|-------|-----------------|
| `list_tables` | `schema` (default `public`) | tables the role can read, with estimated row count and total size including indexes |
| `get_schema` | `table`, `schema` | columns with types, nullability and defaults, constraints |
| `list_indexes` | `schema`, optional `table` | index definitions, size, unique / primary / valid flags, whether the index backs a constraint, scans and last scan, `stats_since` |
| `explain` | `query` | estimated plan (`EXPLAIN (FORMAT JSON, SETTINGS)`), total cost and rows; the query is not executed |
| `explain_analyze` | `query`, `runs` (1–3) | actual plan with timing and buffers per run, or `too_expensive` / `timeout` |
| `hypothetical_index` | `query`, 1–5 `CREATE INDEX` statements | cost before and after, whether the planner uses each index, HypoPG size estimate; `unverifiable` for GIN / GiST |

### Configuration

One environment variable, also present in `.env.example`:

```bash
INDEXRX_DATABASE_URL=postgres://indexrx_agent:<AGENT_PASSWORD>@127.0.0.1:5432/indexrx?sslmode=disable
```

### Build

```bash
go build -o bin/indexrx-mcp ./cmd/indexrx-mcp
go test ./...
```

cgo has to be enabled (the default when a C compiler is installed), so a plain `GOOS=...` cross-build does not work without a C cross-compiler. On Windows, build and run the server inside WSL.

### Try it with MCP Inspector

Web UI (opens a browser; switch the `indexrx-mcp` server on, then open Tools):

```bash
set -a; source .env; set +a
npx @modelcontextprotocol/inspector -e INDEXRX_DATABASE_URL="$INDEXRX_DATABASE_URL" ./bin/indexrx-mcp
```

CLI mode, without a browser:

```bash
npx @modelcontextprotocol/inspector --cli -e INDEXRX_DATABASE_URL="$INDEXRX_DATABASE_URL" ./bin/indexrx-mcp --method tools/list
npx @modelcontextprotocol/inspector --cli -e INDEXRX_DATABASE_URL="$INDEXRX_DATABASE_URL" ./bin/indexrx-mcp --method tools/call --tool-name list_tables
```

If the web UI loads but connecting to the server times out after 30 s while the CLI mode works, make sure nothing between the browser and `127.0.0.1` is interfering. If the browser opens a tab without a fresh token, start with `MCP_AUTO_OPEN_ENABLED=false` and open the URL printed in the terminal.

## Agent role and security

The agent connects as `indexrx_agent` ([`db/init/03_roles.sh`](db/init/03_roles.sh)): `SELECT` only, `default_transaction_read_only`, statement, lock and idle-transaction timeouts, `temp_file_limit`, a connection limit, no access to other databases.

[`docs/agent-role-security.md`](docs/agent-role-security.md) records 38 attempts to break it (writes and DDL, bypassing read-only, timeouts, locks, files, other sessions, query texts) with the prediction, the result and what stopped it. The main conclusions:

- The grants guarantee that the agent cannot change data or schema, read server files or escalate privileges. `read_only` alone is not a barrier: the role can turn it off.
- Session settings (timeouts, `work_mem`) and its own role defaults can be changed by the agent, and several statements in one simple-protocol message escape the server's transaction. The server must open every transaction itself (`BEGIN READ ONLY`, `SET LOCAL` limits, one parsed statement, `ROLLBACK`).
- The agent does not get a "run any SQL" tool. Tools are fixed queries with parameters; arbitrary SQL only appears in `EXPLAIN` of the analyzed query, and `EXPLAIN ANALYZE` is the one point that needs the full protection.

The same document tracks [which requirements the server already meets](docs/agent-role-security.md#implementation-status-of-the-server-requirements) and the [open tasks](docs/agent-role-security.md#open-tasks).

## Repository layout

```
cmd/indexrx-mcp/   MCP server entry point
internal/config/   configuration from the environment
internal/db/       connection pool, read-only transaction helpers
internal/sqlguard/ validation of agent SQL on the PostgreSQL parse tree
internal/tools/    MCP tools
db/init/           extensions, schema, agent role (run on first start of an empty volume)
db/seed/           data generation
docker/            PostgreSQL 17 image with HypoPG
eval/              cases, set-up scripts, results, reference answers
docs/              security review of the agent role
scripts/           seed, explain, ad-hoc SQL
```
