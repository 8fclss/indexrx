# Experiment Results

Measurement table. Time is cold/warm, ms. Buffers — shared hit+read.

| # | Problem | Before (ms / buffers) | Solution | After (ms / buffers) | Index size |
|---|---------|-----------------------|----------|----------------------|------------|
| 01 | Last 20 orders of one user within 90 days (filter + `ORDER BY` + `LIMIT`): Parallel Seq Scan + Sort reads the whole table to return 20 rows | 174 / 29 ms; 18624 | `CREATE INDEX ON orders (user_id, created_at DESC);` Index Scan, no Sort or Gather | 0.33 / 0.05 ms; 26 | 60 MB |
| 02 | Rare status (`pending`, 0.5% of rows): Parallel Seq Scan reads the whole table to return ~10k rows | 32 / 29 ms; 18510 | `CREATE INDEX orders_pending_idx ON orders (id) INCLUDE (user_id, total) WHERE status = 'pending';` + `VACUUM orders;` Index Only Scan | 0.89 / 0.75 ms; 52 | 416 kB |
| 03 | Case-insensitive search by `lower(email)`: Parallel Seq Scan (1 worker) reads the whole table to return 1 row | 33 / 20 ms; 1860 | `CREATE INDEX users_email_lower_idx ON users (lower(email));` + `ANALYZE users;` Index Scan | 0.038 / 0.030 ms; 4 | 7960 kB |
| 04 | Substring search with a leading wildcard (`LIKE '%9999@%'`): Seq Scan reads the whole table to return 20 rows | 13 / 10 ms; 1860 | `CREATE EXTENSION pg_trgm;` `CREATE INDEX users_email_trgm_idx ON users USING gin (email gin_trgm_ops);` Bitmap Heap Scan over Bitmap Index Scan | 0.44 / 0.43 ms; 206 | 6976 kB |
| 05 | | | | | |
| 06 | | | | | |

## Notes

Hypotheses written before the runs and adjusted after the measurements.

- **01 (confirmed):** I expected the plan to go from `Limit -> Gather Merge -> Sort -> Parallel Seq Scan` to `Limit -> Index Scan` with both conditions in `Index Cond` and no `Sort`, all happened accordingly. Trying different indexes resulted in: 
    - *(user_id)* alone — Bitmap Heap Scan + top-N Sort; ~0.54 ms warm time and 18 MB index size
    - *(created_at, user_id)* — Turned out to be not as bad as I thought initially, but still no `Sort` and fewer buffers. The index is walked backwards by date and checks `user_id` inside the index, so the heap is touched only 20 times, but ~300 index pages are read to find 20 hits.
    - *(user_id, created_at)* — ASC and DESC on created_at actually give the same result and size here: the planner reads the ASC index backwards.
- **02 (partly confirmed):** My hypothesis was that instead of `Seq Scan` (before) I could use a partial index `(status) WHERE status = 'pending'` which would allow for a `Bitmap Heap Scan` with only a few thousand buffers. I expected it to make a 3–5x speedup, wwith index well under 1 MB and no need for `VACUUM`. What turned out to be a better solution is a covering index (`(id) INCLUDE (user_id, total) WHERE ...`) but that was not quite what I had in mind, so at first I ran my own variant:

    | Index | Plan | Buffers | Time, ms (cold / warm) | Size |
    |-------|------|---------|------------------------|------|
    | none | Parallel Seq Scan | 18510 | 32 / 29 | – |
    | `(status) WHERE status = 'pending'` (my hypothesis) | Index Scan | 7730 | 27 / 4.8 | 88 kB |
    | `(id) INCLUDE (user_id, total) WHERE status = 'pending'` + `VACUUM` | Index Only Scan | 52 | 0.89 / 0.75 | 416 kB |

    - **Right:** the `Seq Scan` before, the partial index idea, "a few thousand buffers" (7730), "well under 1 MB" (88 kB), and no `VACUUM` needed for this index.
    - **Wrong node:** I expected `Bitmap Heap Scan`, the planner chose a plain `Index Scan`. Not sure, but if I were to guess: all entries of this index have the same key (`pending`), so they are stored in table order and the scan walks the table pages in order, each once, which makes a plain Index Scan about as cheap as a bitmap.
    - **Time:** warm it was 6x better (29 -> 4.8 ms), but the cold run (27 ms) actually was barely faster than the seq scan (32 ms), because 7.7k scattered pages must be read from disk.
    - **What I missed:** the index does not have to be a lookup structure only. If it also contains the columns the query selects (`INCLUDE`), the table is not touched at all: 7730 -> 52 buffers, ~150x less than my own variant, and ~350x less than the seq scan. The 52 buffers are exactly the whole 416 kB index (52 pages of 8 kB): every entry of a partial index matches the query.
    - **Takeaway:** in my index `status` is the same in every entry, so it adds nothing; the `WHERE` predicate does the filtering. What the indexed columns must do is serve what the query needs.
        - Time reduction has a ceiling: returning 10k rows still costs ~0.5 ms, so in the end buffers show the win more honestly (18510 -> 52, ~350x) than time (29 -> 0.75 ms, ~40x).
        - The estimate was good (`rows=9733` vs 10061 actual) because all four statuses are in the most common values (MCV) list.
    - **Not tested:** what `Index Only Scan` does with a stale visibility map (`VACUUM` was run before the first measurement), and the full index on `status`.
- **03 (mostly confirmed, `ANALYZE` was unexpected):** My hypothesis was a `Seq Scan` (probably without workers) and something like 30-40 ms before; an index on the expression `lower(email)` because a index on `email` would just not fit the query; after it an `Index Scan` with under 10 buffers and under 1 ms; an index of a few MB, smaller than the table; and no need for `ANALYZE`, because the planner would see that only one row matches.
    - **Right:** the expression index, `Index Scan`, 4 buffers instead of 1860, ~0.03 ms instead of 20–33 ms, and the index (7960 kB) is smaller than the table (1860 pages = ~14.5 MB).
    - **Detail I got wrong:** the seq scan is `Parallel` with 1 worker, not a `Seq Scan`. I guess the table is just big enough for the planner to use a worker.
    - **Wrong:** `ANALYZE` matters. Right after `CREATE INDEX` the planner still estimated `rows=1000` and chose `Bitmap Heap Scan` (cost 1657). After `ANALYZE users` the estimate became `rows=1` and the plan became `Index Scan` (cost 8.44). Without statistics on `lower(email)` the planner has no base to estimate on and it uses default guess of 0.5% of the table (1000 rows of 200k); `ANALYZE` is what collects statistics on the indexed expression.
    - In this query the wrong estimate costed nothing: the bitmap plan read the same 4 buffers and took 0.056 ms against 0.038 ms. It would probably matter more when this query is a part of some bigger one (a join, for example) where the row estimate drives the choice of the join method.
    - **Plain index on `email`:** not used, the plan stayed `Parallel Seq Scan` with exactly the same cost (4724.71) and 1860 buffers. The planner does not know that `lower(email)` is related to `email` so the indexed expression has to match the one in the query.
- **04 (mostly confirmed, size was unexpected):** My hypothesis was a `Seq Scan` and 10–20 ms before, because a B-tree cannot search for a substring; a GIN index with `pg_trgm`; after it a `Bitmap Heap Scan` over a `Bitmap Index Scan` with buffers dropping to tens and a time of ~1 ms; an index around the size of the table or larger; and I did not know whether the planner would use the index on such a small table.
    - **Right:** `Seq Scan` with 1860 buffers and 10–13 ms; the `pg_trgm` GIN index; the `Bitmap Heap Scan` plan; and the planner does use the index even on a table this small (cost 99.75 against 4360 for the seq scan).
    - **Plain index on `email`:** not used, the plan stayed a `Seq Scan` with the same cost (4360) and 1860 buffers. A B-tree is sorted from the first character, so it cannot help when the pattern starts with `%`.
    - **Buffers were hundreds, not tens:** 206 instead of 1860 (9x), of which 200 are table pages and 6 are index pages. Time went from 10 ms to 0.43 ms (~24x warm), inside my 10–100x guess.
    - **Why 200 table pages for 20 rows:** the index returned 200 candidate rows (`rows=200` in the `Bitmap Index Scan`) and the recheck threw away 180 (`Rows Removed by Index Recheck: 180`). A trigram index only knows that an email contains the trigrams `999` and `99@`, not that they form `9999@` together. So it also returned the 180 ids that end in `999` but not in `9999`, and each candidate sits on its own table page.
    - **Wrong about the size:** the index (6976 kB) is smaller than the table (15 MB), about 46% of it not equal or larger.