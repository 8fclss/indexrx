# Experiment Results

Measurement table. Time is cold/warm, ms. Buffers — shared hit+read.

| # | Problem | Before (ms / buffers) | Solution | After (ms / buffers) | Index size |
|---|---------|-----------------------|----------|----------------------|------------|
| 01 | Last 20 orders of one user within 90 days (filter + `ORDER BY` + `LIMIT`): Parallel Seq Scan + Sort reads the whole table to return 20 rows | 174 / 29 ms; 18624 | `CREATE INDEX ON orders (user_id, created_at DESC);` Index Scan, no Sort or Gather | 0.33 / 0.05 ms; 26 | 60 MB |
| 02 | Rare status (`pending`, 0.5% of rows): Parallel Seq Scan reads the whole table to return ~10k rows | 32 / 29 ms; 18510 | `CREATE INDEX orders_pending_idx ON orders (id) INCLUDE (user_id, total) WHERE status = 'pending';` + `VACUUM orders;` Index Only Scan | 0.89 / 0.75 ms; 52 | 416 kB |
| 03 | Case-insensitive search by `lower(email)`: Parallel Seq Scan (1 worker) reads the whole table to return 1 row | 33 / 20 ms; 1860 | `CREATE INDEX users_email_lower_idx ON users (lower(email));` + `ANALYZE users;` Index Scan | 0.038 / 0.030 ms; 4 | 7960 kB |
| 04 | Substring search with a leading wildcard (`LIKE '%9999@%'`): Seq Scan reads the whole table to return 20 rows | 13 / 10 ms; 1860 | `CREATE EXTENSION pg_trgm;` `CREATE INDEX users_email_trgm_idx ON users USING gin (email gin_trgm_ops);` Bitmap Heap Scan over Bitmap Index Scan | 0.44 / 0.43 ms; 206 | 6976 kB |
| 05 | Containment search in `jsonb` (`payload @> '{"page": "/p/123"}'`, ~0.1% of 3M rows) under `count(*)`: Parallel Seq Scan reads the whole table | 444 / 136 ms; 43603 | `CREATE INDEX events_payload_gin_idx ON events USING gin (payload);` Bitmap Heap Scan over Bitmap Index Scan | 16.8 / 8.3 ms; 3732 | 24 MB |
| 06 | Join of `orders` (filter `user_id = 42`) and `order_items` (4M rows) with no indexes: Parallel Hash Join with two Parallel Seq Scans reads both tables | 441 / 140 ms; 48118 | `CREATE INDEX orders_user_id_idx ON orders (user_id);` + `CREATE INDEX order_items_order_id_idx ON order_items (order_id);` Nested Loop with Index Scan on `order_items` | 32.6 / 3.9 ms; 5966 | 18 MB + 64 MB |

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
- **05 (mostly confirmed, yet again size was unexpected):** My hypothesis was a `Parallel Seq Scan` under an `Aggregate` (because of `count(*)`) with tens of thousands of buffers, more than `orders`, and a few hundred ms before; a GIN index `gin (payload)`; after it a `Bitmap Heap Scan` with a few thousand buffers and a time ~10x better; the planner using the index without trouble; and a big index, maybe over 100 MB.
    - **Right:** `Finalize Aggregate -> Gather -> Partial Aggregate -> Parallel Seq Scan` with 43603 buffers (the table is 341 MB, so it is the whole table) and 444 ms cold / 136 ms warm; the GIN index; `Bitmap Heap Scan`; 3732 buffers after (~12x fewer); time 26x better cold and 16x warm, even better than my guess; and the planner took the index right away (cost 1103 against 60228).
    - **Wrong about the size:** the index is 24 MB, about 7% of the table, not over 100 MB. I assumed GIN would be heavy because it indexes everything inside the documents, but, really, the lists of row addresses in GIN are stored compressed.
    - **Where the 3732 buffers go:** 2938 table pages (`Heap Blocks: exact=2938`, one for almost every one of the 3022 matching rows, since they are scattered) and 794 index pages.
    - **Estimate was off by 10x, downwards:** `rows=283` in the scan against 3022 actual, both with the seq scan (118 per worker) and with the index. The planner has no statistics about what is inside the `jsonb` documents, so it guesses. It did not change the plan here: the index plan was far cheaper anyway.
    - The first run read everything from disk (`read=43603`); in the second one almost everything was still `read` (43507), because the table (341 MB) does not fit in `shared_buffers`, but the OS cache made it 3x faster (444 -> 136 ms). With the index the second run was all `hit`.
    - **`jsonb_path_ops` operator class** (`CREATE INDEX events_payload_path_idx ON events USING gin (payload jsonb_path_ops);`), same query, tested afterwards:

        | GIN operator class | Plan | Buffers (index + table) | Time, ms | Size |
        |--------------------|------|-------------------------|----------|------|
        | none | Parallel Seq Scan | 43603 | 444 / 136 | – |
        | `jsonb_ops` (default) | Bitmap Heap Scan | 3732 (794 + 2938) | 16.8 / 8.3 | 24 MB |
        | `jsonb_path_ops` | Bitmap Heap Scan | 2942 (4 + 2938) | 2.9 / 2.9 | 14 MB |

        - Same plan, same 2938 table pages, but only 4 index pages instead of 794, so the whole difference is in the index part. The index is also 40% smaller (14 MB against 24 MB), and the query ~3x faster than with the default class (2.9 against 8.3 ms warm).
        - A guess why, not checked: `jsonb_ops` stores keys and values as separate entries, so for this query it has to go through the list for the key `page` (every one of the 3M rows has it) as well as the value `/p/123`; `jsonb_path_ops` stores one entry for the pair `page` + `/p/123` with only the 3022 matching rows in its list.
        - The price: `jsonb_path_ops` supports only containment-like operators (`@>`, `@?`, `@@`), not key-exists ones (`?`, `?|`, `?&`). For a query that only uses `@>`, like this one, it is totally better choice. For the final result I chose the default option due to its versatility.
- **06 (confirmed, one index alone is an extra finding):** My hypothesis was a `Hash Join` of two `Seq Scan`s (probably parallel) with ~50k buffers and a few hundred ms before; indexes on `orders (user_id)` and `order_items (order_id)` created together; after them a `Nested Loop` with `Index Scan`s (or a `Bitmap Heap Scan` for `orders`), a few thousand buffers and 10–30 ms; `order_items` index bigger than the `orders` one. I did not make an assumption about each index size alone, so that part I just measured.
    - **Right:** `Parallel Hash Join` over two `Parallel Seq Scan`s, 48118 buffers (18510 pages of `orders` + 29412 of `order_items`) and 441 ms cold / 140 ms warm; after the indexes a `Nested Loop` with a `Bitmap Heap Scan` on `orders` and an `Index Scan` on `order_items` (`loops=993`), 5966 buffers (8x fewer), 32.6 ms cold / 3.9 ms warm (36x).
    - **Slightly off:** `order_items` has twice the rows of `orders`, but it is only 1.6x the pages (230 MB against 145 MB), because its rows are narrower. The indexes are 18 MB and 64 MB: the `order_items` one is bigger as I expected. If I were to guess before the measurements, I would probably assumed the indexes' size to be a little more than that. But probably it is because a user has many orders with the same `user_id`, and B-tree stores such duplicates compactly (not checked) which results in such a size.
    - **Where the 5966 buffers go:** 973 for `orders` (966 table pages + 7 index pages) and 4993 for the inner `Index Scan`, which runs 993 times, once for each order of the user: about 5 buffers per lookup (3 index levels + ~2 table pages for the ~2 items of an order).
    - **ANALYZE was not needed:** I did not run it, and the estimates were already close (`rows=733` against 993 for `orders`, 1466 against 2014 for the join), the plan was a good one from the start.
    - **Each index alone** (same query, warm run):

        | Indexes | Plan | Buffers | Time, ms |
        |---------|------|---------|----------|
        | none | Parallel Hash Join, 2 Seq Scans | 48118 | 140 |
        | only `order_items (order_id)` | Nested Loop: Parallel Seq Scan on `orders` + Index Scan on `order_items` | 23505 | 29 |
        | only `orders (user_id)` | Hash Join: Bitmap Heap Scan on `orders` + Parallel Seq Scan on `order_items` | 32431 | 110 |
        | both | Nested Loop: Bitmap Heap Scan + Index Scan | 5966 | 3.9 |

    - **Neither index is enough alone:** each one removes only one of the two table scans. With only the `order_items` index the table `orders` is still read in full (~18k pages) to find the 993 orders. With only the `orders` index the join still reads the whole `order_items` (~29k pages) and builds a hash on the other side. The effect of the two together (3.9 ms) is much bigger than of each separately (29 and 110 ms).
    - **The `order_items` index matters more:** the bigger table is the main cost, so it alone gives 29 ms against 110 ms for the other one. Buffers show it only partly (23505 against 32431), the time gap is bigger because the leftover seq scan handles 2M rows of `orders` in one case and 4M rows of `order_items` in the other, while each of them also going through the hash join (not actually measured separately).
    - In the `orders`-only variant the planner chose a `Hash Join` and every one of the 3 processes built its own hash from the `Bitmap Heap Scan` (`loops=3`), so the `orders` part was read 3 times.