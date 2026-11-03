# Security of the agent role

What the database itself guarantees for `indexrx_agent` and what the server has to guarantee on top of it.

## Results

Legend: **blocked** = the attempt failed as intended; **passed** = the attempt worked and is a finding; **works** = expected behaviour.

### Group 1. Writes and DDL

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 1 | `DROP TABLE orders;` | read-only error | **blocked**: `cannot execute DROP TABLE in a read-only transaction` | `read_only`. Utility commands are checked for read-only before ownership. Inside `BEGIN READ WRITE`: `must be owner of table orders` (ownership) |
| 2 | `DELETE FROM orders WHERE id = 1;` | read-only error | **blocked**: `cannot execute DELETE in a read-only transaction` | `read_only`; with read-only off: `permission denied for table orders` (no `DELETE` grant) |
| 3 | `EXPLAIN ANALYZE DELETE FROM orders WHERE id = 1;` | read-only error first | **blocked**: `cannot execute DELETE in a read-only transaction` | The executor checks "may this transaction write" at start, before it initializes the plan and checks table permissions. Plain `EXPLAIN DELETE` (not executed) skips the read-only check and fails on permissions: `permission denied for table orders` |
| 4 | `CREATE INDEX ON orders (total);` | read-only error | **blocked**: `cannot execute CREATE INDEX in a read-only transaction` | `read_only`; inside `BEGIN READ WRITE`: `must be owner of table orders` |
| 5 | `CREATE TABLE tmp_test (id int);` | read-only error | **blocked**: `cannot execute CREATE TABLE in a read-only transaction` | `read_only`; inside `BEGIN READ WRITE`: `permission denied for schema public` (no `CREATE` on `public` since PG 15) |
| 6 | `CREATE TEMP TABLE tmp_test (id int);` | read-only error | **blocked**: `cannot execute CREATE TABLE in a read-only transaction` | `read_only`; inside `BEGIN READ WRITE`: `permission denied to create temporary tables in database "indexrx"` (the `TEMP` privilege of `PUBLIC` was removed by `REVOKE ALL ON DATABASE`). See 34: not so in other databases |
| 7 | `SELECT * FROM orders LIMIT 1 FOR UPDATE;` | read-only or permission error | **blocked**: `cannot execute SELECT FOR UPDATE in a read-only transaction` | `read_only`; inside `BEGIN READ WRITE`: `permission denied for table orders` (row locks need `UPDATE`) |

### Group 2. Bypassing read-only

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 8 | `BEGIN READ WRITE; DELETE FROM orders WHERE id = 1; ROLLBACK;` | read-only bypassed, permission error | read-only **passed**, write **blocked**: `permission denied for table orders` | Grants. `BEGIN READ WRITE` overrides the role default |
| 9 | `SET default_transaction_read_only = off;` then `DELETE`, `UPDATE`, `INSERT`, `TRUNCATE` | allowed, then permission error | `SET` **passed** (`transaction_read_only = off`), all writes **blocked**: `permission denied for table orders` | Grants. The setting is only a session default, any role may change it |
| 10 | `BEGIN READ ONLY; SELECT set_config('transaction_read_only', 'off', true);` and `SET TRANSACTION READ WRITE` after a query | – | **blocked**: `transaction read-write mode must be set before any query` | PostgreSQL itself: once a transaction has taken a snapshot (any `SELECT`, including the one that calls `set_config`), it cannot become read-write. `SET` and `SHOW` do not take a snapshot, so this lock-in starts only with the first real query (see 11). A transaction opened by the server as `BEGIN READ ONLY` stays read-only, as long as the agent cannot end it (see 35) |
| 11 | Server-side wrapper: `BEGIN READ ONLY; SET LOCAL statement_timeout = '5s';`, then the agent statement `SET TRANSACTION READ WRITE;` | – | **passed**: `transaction_read_only = off`, and a following `ALTER ROLE indexrx_agent SET statement_timeout = 0` in the same transaction succeeded (rolled back by the test). Same with `SET LOCAL transaction_read_only = off`, and after a `SHOW`. `SELECT set_config('transaction_read_only', 'off', true)` after the server's `SET LOCAL` is still **blocked** (`transaction read-write mode must be set before any query`) | `SET LOCAL` and `SHOW` do not take a snapshot, so the transaction can still switch to read-write after them. The server's own `SET LOCAL` statements do not lock read-only in. Harmful only if the agent gets a second statement in the same transaction (35, or a server that runs several agent statements per transaction). Another reason to let through only one statement that the parser classifies as `SELECT` / `EXPLAIN`, never `SET` / `SET TRANSACTION` / `BEGIN` / `COMMIT` |

**Conclusion of groups 1–2:** writes are prevented by the grants (only `SELECT`, no ownership, no `CREATE` on schema and database). `default_transaction_read_only` is not a barrier, because the agent can turn it off. It is a second layer: it fails early with a clear message, and it would still stop writes if a grant were given by mistake. It only works if the transaction is opened read-only by the server, not by the role default.

### Group 3. Timeouts and resources

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 12 | `SELECT pg_sleep(10);` | canceled after 5 s | **blocked**: `canceling statement due to statement timeout` after 5000 ms | Role default `statement_timeout = 5s` |
| 13 | `SET statement_timeout = 0;` then `SELECT pg_sleep(10);` | passes | **passed**: 10006 ms | `statement_timeout` is a user-level setting, any role may change it |
| 14 | `SELECT set_config('statement_timeout', '0', false);` then `SELECT pg_sleep(10);` | passes | **passed**: 10001 ms | The same setting through a function. A filter that only allows statements starting with `SELECT` does not stop it. It also works hidden in a CTE (`WITH s AS (SELECT set_config(...)) SELECT ... FROM s`). It does not lift the limit for the statement that calls it (`SELECT set_config(...), pg_sleep(7)` was canceled at 5 s), only for the following ones |
| 15 | `SELECT set_config('default_transaction_read_only', 'off', false);` | – | **passed** | Same as 9, through a `SELECT` |
| 16 | `SET work_mem = '8GB';` (also `hash_mem_multiplier = 1000`, `maintenance_work_mem = '8GB'`, `max_parallel_workers_per_gather = 8`) | allowed | **passed**, all accepted | User-level settings. `work_mem` is allocated per sort/hash node and per process: one query with a few sorts in a parallel plan can try to take tens of GB and get the server killed by the OOM killer, which restarts all connections. `temp_file_limit` (25) could not be changed |
| 17 | 6 sessions as the agent at the same time | 6th rejected | **blocked**: `FATAL: too many connections for role "indexrx_agent"`; the main user could still connect | `CONNECTION LIMIT 5` |
| 18 | `BEGIN READ WRITE; ALTER ROLE indexrx_agent SET statement_timeout = 0;` (or after `SET default_transaction_read_only = off`) | probably passes | **passed**, and it is **persistent**: `ALTER ROLE indexrx_agent SET statement_timeout = 0` and `SET default_transaction_read_only = off` were stored in `pg_roles.rolconfig` and applied to every new agent session. **`ALTER ROLE indexrx_agent PASSWORD 'hacked'` also passed.** Only `CONNECTION LIMIT` was refused (`permission denied to alter role`) | PostgreSQL lets every role change its own session defaults and its own password. This removes all role-level limits for all future sessions, and a password change locks the server out (denial of service). Restored by the admin after the test. **Needs a read-write transaction:** inside `BEGIN READ ONLY` both `ALTER ROLE ... SET` and `ALTER ROLE ... PASSWORD` fail with `cannot execute ALTER ROLE in a read-only transaction`. If the server opens `BEGIN READ ONLY` itself and passes exactly one agent statement, the database blocks the most dangerous attack of this list by itself (the only gap is 11) |
| 19 | `SET lock_timeout = 0; SET idle_in_transaction_session_timeout = 0; SET idle_session_timeout = 0;` | allowed | **passed** | User-level settings: the protection of group 4 can be turned off by the agent |
| 20 | `SET ROLE indexrx;` / `SET SESSION AUTHORIZATION indexrx;` | permission error | **blocked**: `permission denied to set role "indexrx"` | The agent is not a member of `indexrx` |
| 21 | `SELECT pg_stat_reset();` / `SELECT pg_stat_statements_reset();` | permission error | **blocked**: `permission denied for function ...` | Superuser-only by default. Important for case 10: the agent cannot wipe usage statistics |
| 22 | `SELECT pg_advisory_lock(42);` | allowed | **passed** | Advisory locks are allowed in read-only transactions and are not tied to a table. Session-level ones stay until unlock or disconnect, **also after `ROLLBACK`** (still held after `BEGIN READ ONLY; SELECT pg_advisory_lock(42); ROLLBACK;`): an agent can block an application that uses the same keys. `pg_advisory_xact_lock` is released by `ROLLBACK` |
| 23 | `BEGIN; LOCK TABLE orders IN ACCESS SHARE MODE;` / `IN ACCESS EXCLUSIVE MODE` | share allowed, exclusive refused | share **passed**, exclusive **blocked**: `permission denied for table orders` (and read-only error without `READ WRITE`) | `ACCESS SHARE` only needs `SELECT`, and it is enough for group 4 |
| 24 | `SELECT pg_current_xact_id();` in a read-only transaction | read-only error | **passed**: returned a new xid | Assigning a transaction id is allowed in read-only transactions. Harmless by itself, but every call consumes an xid (in a loop: xid wraparound pressure) |
| 25 | `SET temp_file_limit = -1; SET log_statement = 'none'; SET session_replication_role = replica;` | permission error | **blocked**: `permission denied to set parameter ...` | Superuser-level settings. `SET jit = on` passed (user-level) |

### Group 4. Locks

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 26 | A (agent): `BEGIN; SELECT count(*) FROM orders;` and idle. B (`indexrx`): `ALTER TABLE orders ADD COLUMN x int;` C: `SELECT id FROM orders LIMIT 1;` as `indexrx` and as the agent | B waits for `AccessExclusiveLock`, C waits behind B; after 30 s A is terminated, B and C continue | **passed for 29 s**, then **stopped**. A holds `AccessShareLock` (granted); B waits for `AccessExclusiveLock` (`blocked_by = {A}`); C as `indexrx` waits for `AccessShareLock` (`blocked_by = {B}`). After 30 s A was terminated (`FATAL: terminating connection due to idle-in-transaction timeout`), B finished after 29.1 s, C after 28.0 s. C as the agent failed after 1 s: `canceling statement due to lock timeout`. Column `x` was dropped afterwards | `idle_in_transaction_session_timeout = 30s` ended it, but only after 30 s of a blocked table. **Why a plain `SELECT` was blocked:** the lock queue is fair: C's `AccessShareLock` does not conflict with A, but it conflicts with B's `AccessExclusiveLock` that is waiting ahead of it, so C queues behind B. One idle agent transaction plus one migration blocks every query on the table. The agent can also turn the 30 s off (19) |

### Group 5. Outside the database

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 27 | `SELECT pg_read_file('/etc/passwd');` (also `pg_ls_dir`, `pg_stat_file`, `lo_import`) | permission error | **blocked**: `permission denied for function pg_read_file` (same for the others) | Superuser / `pg_read_server_files` only |
| 28 | `COPY orders TO '/tmp/orders.csv';` (also `COPY ... FROM '/tmp/x.csv'`, `COPY (SELECT 1) TO PROGRAM 'id'`) | permission error | **blocked**: `permission denied to COPY to a file` / `from a file` / `to or from an external program` | `pg_write_server_files`, `pg_read_server_files`, `pg_execute_server_program` not granted |
| 29 | `COPY (SELECT * FROM orders LIMIT 5) TO STDOUT;` | works | **passed**: 5 rows | It is a `SELECT` that streams to the client. Nothing stops `SELECT * FROM users` (200k emails) either: the agent can read every row of every table, so whatever the server forwards to the model is a data leak path |
| 30 | `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = 'indexrx';` (also `pg_cancel_backend`) | permission error | **blocked**: `permission denied to terminate process`, *Only roles with the SUPERUSER attribute may terminate processes of roles with the SUPERUSER attribute* | Only because `indexrx` is a superuser. Without `pg_signal_backend` a role can still signal sessions of roles it is a member of |
| 31 | `pg_terminate_backend` on other `indexrx_agent` sessions | – | **passed**: `t`, the other agent session got `FATAL: terminating connection due to administrator command` | A role may terminate its own role's sessions. One agent request can kill all other agent sessions in the pool |
| 32 | `SELECT query FROM pg_stat_activity WHERE usename = 'indexrx';` | – | **passed**: full current query text of the main user, with literals (`... email=bob@example.com ...`) | `pg_read_all_stats` |
| 33 | `SELECT query FROM pg_stat_statements LIMIT 20;` | sees all users' queries | First run: **passed**. Queries of `indexrx` are visible. Literals in DML are normalized (`$1`), but **utility statements are stored as typed**: `CREATE ROLE indexrx_agent LOGIN PASSWORD '...'` and `ALTER ROLE indexrx_agent PASSWORD '...'` were visible with the **password in plain text**. After the fix (`SET pg_stat_statements.track_utility = off` at the start of `03_roles.sh`), on a fresh init: **blocked**, 0 rows with `PASSWORD`, no `CREATE ROLE` / `ALTER ROLE` at all. Still **passed**: queries of other users are visible, and an `ALTER ROLE indexrx_agent PASSWORD '...'` run later by an admin in an ordinary session is stored again with the password | `pg_read_all_stats` gives access to all query texts. `pg_stat_statements.track = all` also tracks utility statements, and those are not normalized. The fix only covers the init session: `track_utility` is set with `SET`, not for the server. Changing passwords later: with `\password` in `psql` (sends a hash) or with `SET pg_stat_statements.track_utility = off` in the same session. Access to other users' query texts: open decision, see the Conclusion |
| 34 | `\c postgres`, `\c template1` | – | First run: **passed**. Connected to both. In `postgres`: `BEGIN READ WRITE; CREATE TEMP TABLE t AS SELECT ... 1000000 rows` → 35 MB written; `CREATE TABLE public.x` blocked (`permission denied for schema public`). After the fix: `\c postgres` **blocked**: `FATAL: permission denied for database "postgres"`, *User does not have CONNECT privilege*. `\c template1` still connects, but `CREATE TEMP TABLE` there is **blocked**: `permission denied to create temporary tables in database "template1"` | First run: `REVOKE ALL ... FROM PUBLIC` was applied only to `indexrx`, `PUBLIC` still had `CONNECT` and `TEMP` on `postgres` and `template1`. Temp tables are not limited by `temp_file_limit`, so the agent could fill the disk there. Fixed in `03_roles.sh`: `REVOKE CONNECT, TEMP ON DATABASE postgres FROM PUBLIC`, `REVOKE TEMP ON DATABASE template1 FROM PUBLIC`. `CONNECT` on `template1` stays (tools such as `createdb` use it); without `TEMP` and without `CREATE` on its `public` schema the agent can only read the empty catalog there |
| 35 | Naive wrapper: server sends `BEGIN READ ONLY`, then the agent string `COMMIT; SET statement_timeout = 0; SET default_transaction_read_only = off; SELECT 1` | – | Simple query protocol: **passed**. The `COMMIT` ended the server's transaction, after that `statement_timeout = 0`, `transaction_read_only = off`, and `pg_sleep(6)` ran on the same (pooled) connection. The settings survived because the agent's `COMMIT` committed them: inside a transaction that is rolled back, `SET` and `set_config(..., false)` are undone. Extended protocol (prepared statement): **blocked**: `cannot insert multiple commands into a prepared statement` | Several statements in one simple query message are executed one after another. Tested with psycopg. pgx `Query` / `QueryRow` use the extended protocol by default; `QueryExecModeSimpleProtocol`, and `Exec` without arguments, send a simple query and would let this through |

### Group 6. What must work

| # | Attempt | Prediction | Result | What stopped it (or why it passed) |
|---|---------|------------|--------|------------------------------------|
| 36 | `SELECT count(*) FROM orders;`, `EXPLAIN (ANALYZE, BUFFERS)` of cases 01 and 05 | works | **works** | `SELECT` grant; `EXPLAIN ANALYZE` of a `SELECT` is allowed in a read-only transaction. It does execute the query, so it is bounded only by `statement_timeout` |
| 37 | `pg_stats` for `orders`, `pg_stat_user_tables`, `pg_stat_user_indexes`, `pg_stat_statements`, `pg_stats_ext` | works | **works**: all columns of `orders` with `n_distinct` / MCV, table and index counters, 95 rows of `pg_stat_statements` | `pg_stats` only shows columns the role may `SELECT`; the statistics views are readable, the full query texts come from `pg_read_all_stats` |
| 38 | `SELECT * FROM hypopg_create_index('CREATE INDEX ON orders (total)');` + `EXPLAIN` in a read-only transaction (also inside `BEGIN READ ONLY`) | works | **works**: `Bitmap Heap Scan` over `<13610>btree_orders_total`. The hypothetical index is not in `pg_class`, not visible to other sessions, and ignored by `EXPLAIN ANALYZE` | Not a contradiction: HypoPG keeps hypothetical indexes in the memory of the backend and adds them to the planner through a hook. Nothing is written to the catalog or to disk, so there is no write for read-only to forbid. They live until `hypopg_reset()` or the end of the session, so the server must reset them (or use a fresh session) between requests |

## Conclusion

### Guaranteed by the database (whatever SQL the agent sends)

- No change of data: `INSERT`, `UPDATE`, `DELETE`, `TRUNCATE`, `MERGE`, `SELECT ... FOR UPDATE/SHARE` fail on missing grants, even with read-only turned off.
- No DDL on existing objects (`DROP`, `ALTER TABLE`, `CREATE INDEX`): not the owner. No new objects in `public` (no `CREATE` on the schema) and no temp tables in `indexrx` (no `TEMP` on the database).
- No access to the server's files or programs: `pg_read_file`, `pg_ls_dir`, `lo_import`, `COPY TO/FROM` a file or `PROGRAM`.
- No escalation: `SET ROLE` / `SET SESSION AUTHORIZATION` to `indexrx`, superuser-level settings (`temp_file_limit`, `log_statement`, `session_replication_role`), `pg_authid` / `pg_shadow`.
- No reset of statistics (`pg_stat_reset`, `pg_stat_statements_reset`).
- No signals to sessions of the superuser `indexrx`.
- At most 5 agent sessions; other roles can still connect.
- A transaction that the server opened as `BEGIN READ ONLY` and in which a query has already taken a snapshot cannot become read-write (10). `SET LOCAL` / `SHOW` do not count (11).
- Inside such a transaction no `ALTER ROLE` is possible (18): the agent cannot change its own role defaults or password without a read-write transaction.
- `ROLLBACK` undoes `SET` and `set_config(..., false)` made in the transaction. Not undone: session-level advisory locks (22) and HypoPG hypothetical indexes (38), they are not transactional.
- No access to the databases `postgres` (no `CONNECT`) and no temp tables in `template1` (34, after the fix).
- With the role defaults in place: 5 s per statement, 1 s per lock wait, 30 s for an idle transaction. **Only as long as the agent does not change them** (see below).

### Not guaranteed by the database: the server must do it

What the agent could do if server passed its SQL through unchanged and the requirement that follows:

1. **Turn off every limit** (13, 14, 15, 16, 19): `SET` / `set_config` / `RESET` for `statement_timeout`, `lock_timeout`, `idle_in_transaction_session_timeout`, `default_transaction_read_only`, `work_mem`, `hash_mem_multiplier`, `max_parallel_workers_per_gather`. `set_config` also works from inside a `SELECT` or a CTE.
   → Server sets the limits itself for every request, inside its own transaction: `BEGIN READ ONLY; SET LOCAL statement_timeout = ...; SET LOCAL lock_timeout = ...; SET LOCAL work_mem = ...;` and does not rely on the role defaults. Reject statements other than `SELECT` / `EXPLAIN` / `WITH ... SELECT` with a real SQL parser (e.g. `pg_query_go`), and reject calls of `set_config`, `pg_sleep`, `pg_advisory_*`, `pg_terminate_backend`, `pg_cancel_backend`, `pg_current_xact_id`/`txid_current`, `dblink*`/`lo_*` anywhere in the tree, not by a prefix check.
2. **Change its own role persistently, including the password** (18). Needs a read-write transaction: blocked by the database itself as long as the server opens `BEGIN READ ONLY` and the agent gets exactly one statement in it (11 shows how a second statement would reopen it).
   → Never let `ALTER ROLE` / `ALTER USER` / any utility statement through: the server only sends parsed single `SELECT` / `EXPLAIN` statements. PostgreSQL has no grant that forbids a role to change its own defaults or password, so the database cannot stop this by itself. As a safety net, Server checks `pg_roles.rolconfig` of `indexrx_agent` at start-up and refuses to run if it differs from the expected settings; with certificate authentication instead of a password, a changed password would not lock the server out.
3. **Escape the server's transaction with several statements** (35): `COMMIT; ...`.
   → Only the extended query protocol (`Query` / `QueryRow` with the default exec mode); one statement per request; reject anything with more than one statement after parsing. Never `QueryExecModeSimpleProtocol` or argument-less `Exec` for agent SQL.
4. **Keep state between requests** (22, 35, 38): settings changed with `SET` or `set_config(..., false)` stay on a pooled connection only if the transaction is committed, as with the agent's `COMMIT` in 34; a `ROLLBACK` undoes them. Not undone by `ROLLBACK`: session-level advisory locks (22) and hypothetical indexes (38).
   → Wrap every request in `BEGIN READ ONLY ... ROLLBACK` and never `COMMIT` agent work: this undoes all settings. Call `hypopg_reset()` explicitly at the end of every request that may have created hypothetical indexes: `DISCARD ALL` does not reset them. `DISCARD ALL` releases advisory locks and resets settings, but cannot run inside a transaction block (`DISCARD ALL cannot run inside a transaction block`), so it belongs in the hook that returns a connection to the pool (pgx `AfterRelease` / `BeforeAcquire`), not in the request. On any error close the connection instead of returning it.
5. **Block the table for others** (26): an idle agent transaction holding `AccessShareLock` plus a waiting migration blocks every `SELECT` for up to 30 s, longer if the agent turns the timeout off.
   → Server never leaves a transaction open between model steps: one request = one short transaction that is ended immediately. Keep `idle_in_transaction_session_timeout` and `lock_timeout` as a safety net (in the role and with `SET LOCAL`) and forbid `SET` from the agent (point 1).
6. **Use a lot of memory or CPU within the time limit** (16, 36): `work_mem = '8GB'`, parallel workers, `EXPLAIN ANALYZE` executes the query.
   → Server sets `work_mem`, `max_parallel_workers_per_gather` with `SET LOCAL` and forbids `SET`; `EXPLAIN ANALYZE` only on queries from the eval/case set or after a plain `EXPLAIN` cost check; limit the number of concurrent agent requests below `CONNECTION LIMIT`.
7. **Kill other agent sessions** (31).
   → Forbid `pg_terminate_backend` / `pg_cancel_backend` (point 1). In the database: separate roles per tenant if sessions belong to different users.
8. **Read all data and other users' queries** (29, 32, 33): every row of every table, full texts of current and past queries of all users. Passwords from the init script no longer appear in `pg_stat_statements` (33, after the fix), but a later `ALTER ROLE ... PASSWORD '...'` by an admin would.
   → Server limits the result size (`LIMIT`, row and byte caps), and filters or masks what is sent to the model. Admins change passwords with `\password` or with `track_utility` turned off in that session. Access to other users' query texts (`pg_read_all_stats`): **open decision**, see below.
9. **Connect to other databases** (34): fixed in `03_roles.sh` for `postgres` and for temp tables in `template1`. `CONNECT` on `template1` remains.
   → Server always connects to `indexrx` only. Any new database on the server needs the same `REVOKE ... FROM PUBLIC`.
10. **Use advisory locks and consume xids** (22, 24).
    → Forbid the functions (point 1).

### Design of the server tools

Almost every bypass above works through a function inside a `SELECT`: `set_config` (14, 15), `pg_sleep` (12–14), `pg_advisory_lock` (22), `pg_terminate_backend` (31), `pg_current_xact_id` (24). A denylist of functions (point 1) is fragile: extensions and future versions add new functions, and the list has to include the HypoPG functions too (`hypopg_create_index`, `hypopg_reset`, ...), otherwise the agent creates hypothetical indexes past the tool that is supposed to manage them.

The more reliable design follows from the table itself: **the agent has no tool "run arbitrary SQL".**

- Almost all tools (`get_schema`, `list_indexes`, table and index statistics, the duplicate-index search of case 09, unused indexes of case 10, `pg_stats` of a column) are fixed queries written in the server code. The agent passes only parameters (a table name, a column name), and the server passes them as bind parameters or checks them against the catalog. Nothing of the agent's text becomes SQL.
- Arbitrary SQL appears in one place only: the query that is being analyzed. There the risk depends on the mode:
  - `EXPLAIN` without `ANALYZE` plans the query but does not execute it: `EXPLAIN SELECT pg_sleep(10)` returned in 2 ms, `EXPLAIN SELECT set_config('statement_timeout', '0', false)` left the setting at 5 s, an advisory lock in a sub-select was not taken. These functions are volatile, and the planner evaluates only immutable functions (and stable ones for estimates) at planning time; the agent cannot create its own functions (no `CREATE` anywhere), so nothing with side effects runs. HypoPG checks also use plain `EXPLAIN`.
  - `EXPLAIN ANALYZE` executes everything, including functions. This is the only really dangerous point, and the protection is concentrated there: a SQL parser, a check that it is exactly one `SELECT` (no `SET`, `BEGIN`, `COMMIT`, DML, utility), the function denylist as an additional layer, `BEGIN READ ONLY` + `SET LOCAL` limits set by the server, `ROLLBACK` and `hypopg_reset()` at the end.

### Open decision: `pg_read_all_stats` (32, 33)

Without this role the agent does not see the query texts of other users in `pg_stat_statements` and `pg_stat_activity`, and they are needed to analyse the workload. With it the agent sees all of them, including literals in current queries (32). The planned direction: replace the role grant with a `SECURITY DEFINER` function that returns only normalized DML statements and the needed counters, and build it together with the server tool for `pg_stat_statements`. Until then the role stays granted.

## Authentication inside the container

`pg_hba.conf` of the official image trusts local socket and container-localhost connections, so `docker compose exec ... psql` never asks for a password.
Connections from the host come from the Docker gateway address and hit `scram-sha-256`. Trust skips authentication only, not authorization: the agent
role keeps all its restrictions. Acceptable for local development; the server must connect over TCP with a password.
