# Experiment Results

Measurement table. Time is cold/warm, ms. Buffers — shared hit+read.

| # | Problem | Before (ms / buffers) | Solution | After (ms / buffers) | Index size |
|---|---------|-----------------------|----------|----------------------|------------|
| 01 | Last 20 orders of one user within 90 days (filter + `ORDER BY` + `LIMIT`): Parallel Seq Scan + Sort reads the whole table to return 20 rows | 174 / 29 ms; 18624 | `CREATE INDEX ON orders (user_id, created_at DESC);` Index Scan, no Sort or Gather | 0.33 / 0.05 ms; 26 | 60 MB |
| 02 | Rare status (`pending`, 0.5% of rows): Parallel Seq Scan reads the whole table to return ~10k rows | 32 / 29 ms; 18510 | `CREATE INDEX orders_pending_idx ON orders (id) INCLUDE (user_id, total) WHERE status = 'pending';` + `VACUUM orders;` Index Only Scan | 0.89 / 0.75 ms; 52 | 416 kB |
| 03 | | | | | |
| 04 | | | | | |
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