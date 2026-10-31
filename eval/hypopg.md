# HypoPG check

Question: can the benefit of an index be shown without creating it? For each case a hypothetical index is created with `hypopg_create_index`, the query goes through plain `EXPLAIN` and the result is compared with the same real index (`EXPLAIN` on the real one, cost from the same database).

```sql
SELECT * FROM hypopg_create_index('CREATE INDEX ON orders (user_id, created_at DESC)');
EXPLAIN SELECT ...;
SELECT * FROM hypopg_reset();
```

## Summary

- **The idea works for 4 of 6 cases (01, 02, 03, 06).** In all 11 supported variants the planner took the hypothetical index and chose the same plan as with the real one. The ranking of variants inside a case is also the same.
- **04 and 05 cannot be checked: HypoPG does not support GIN.** GiST and SP-GiST are not supported either.
- **Cost is close for most variants (within ±3%), but not for all:** `(created_at, user_id)` is +40% and the covering index in 02 is +51%. The plan is still the same.
- **Index size is overestimated by 1.2x–3.4x for B-tree.** It should not be shown to the user as a number.
- **Expression index: no statistics.** The plan in 03 is not the final one, but the one a real index gets *without* `ANALYZE`.

## 1. Plan and cost: hypothetical against real

| # | Index | Hypothetical (plan, cost) | Real (plan, cost) | Cost diff | Same plan |
|---|-------|---------------------------|-------------------|-----------|-----------|
| 01 | none | Parallel Seq Scan + Sort, 36178 | – | – | – |
| 01 | `(user_id, created_at DESC)` | Index Scan, 20.14 | Index Scan, 20.51 | -1.8% | yes |
| 01 | `(user_id, created_at)` | Index Scan, 20.14 | Index Scan, 20.51 | -1.8% | yes |
| 01 | `(user_id)` | Sort + Bitmap Heap Scan, 117.46 | Sort + Bitmap Heap Scan, 117.83 | -0.3% | yes |
| 01 | `(created_at, user_id)` | Index Scan, 8801.54 | Index Scan, 6297.91 | +40% | yes |
| 02 | none | Parallel Seq Scan, 30901 | – | – | – |
| 02 | `(status) WHERE status = 'pending'` | Index Scan, 10879.00 | Index Scan, 10771.25 | +1.0% | yes |
| 02 | `(id) INCLUDE (user_id, total) WHERE status = 'pending'` | Index Only Scan, 530.03 | Index Only Scan, 350.28 | +51% | yes |
| 03 | none | Parallel Seq Scan, 4724.71 | – | – | – |
| 03 | `(email)` | Seq Scan (not used), 4724.71 | Seq Scan (not used), 4724.71 | 0% | yes |
| 03 | `(lower(email))` | Bitmap Heap Scan, 1665.16, `rows=1000` | without `ANALYZE`: Bitmap Heap Scan, 1657.54 | +0.5% | yes |
| 03 | `(lower(email))` | the same, nothing to `ANALYZE` | with `ANALYZE users`: Index Scan, 8.44, `rows=1` | – | **no** |
| 03 | `USING hash (lower(email))` | Bitmap Heap Scan, 1653.12 | Bitmap Heap Scan, 1657.12 | -0.2% | yes |
| 04 | `USING gin (email gin_trgm_ops)` | **error: access method "gin" is not supported** | Bitmap Heap Scan, 99.75 (Seq Scan: 4360) | – | – |
| 05 | `USING gin (payload)` | **error: access method "gin" is not supported** | Bitmap Heap Scan, 1106.6 (Seq Scan: 60230) | – | – |
| 06 | none | Parallel Hash Join, 80614 | – | – | – |
| 06 | only `order_items (order_id)` | Nested Loop, 37396.23 | Nested Loop, 37518.24 | -0.3% | yes |
| 06 | only `orders (user_id)` | Hash Join, 55321.32 | Hash Join, 55309.70 | 0.0% | yes |
| 06 | both | Nested Loop, Bitmap Heap Scan + Index Scan, 15077.86 | the same, 15238.39 | -1.1% | yes |
| 06 | both, `order_items` with `INCLUDE (product_id, quantity)` | Nested Loop, Index Only Scan, 7574.92 | the same, 7758.97 | -2.4% | yes |

## 2. Which solutions HypoPG supports

| Feature | Result | Used in |
|---------|--------|---------|
| `btree`, `ASC`/`DESC`, composite | works | 01, 02, 06 |
| partial (`WHERE`) | works, the planner uses it | 02 |
| `INCLUDE` | works, gives an Index Only Scan | 02, 06 |
| expression (`lower(email)`) | works, but with no statistics on the expression (see above) | 03 |
| `hash` | works | 03 (alternative) |
| `brin`, `bloom` | the index is created, not tested in plans | – |
| `gin` (`gin_trgm_ops`, `jsonb_ops`, `jsonb_path_ops`) | **not supported:** `hypopg: access method "gin" is not supported` | 04, 05 |
| `gist` (`gist_trgm_ops`) | **not supported** | 04 (alternative) |
| `spgist` | **not supported** | – |

## 3. Index size: `hypopg_relation_size` against real

| Case | Index | Hypothetical | Real | Ratio |
|------|-------|--------------|------|-------|
| 01 | `(user_id, created_at DESC)` | 99.7 MB | 60.2 MB | 1.66x |
| 01 | `(created_at, user_id)` | 99.7 MB | 60.1 MB | 1.66x |
| 01, 06 | `(user_id)` | 59.8 MB | 17.7 MB | 3.38x |
| 02 | `(status) WHERE status = 'pending'` | 304 kB | 88 kB | 3.45x |
| 02 | `(id) INCLUDE (user_id, total) WHERE status = 'pending'` | 768 kB | 416 kB | 1.85x |
| 03 | `(email)` | 9688 kB | 7960 kB | 1.22x |
| 03 | `(lower(email))` | 9688 kB | 7960 kB | 1.22x |
| 03 | `USING hash (lower(email))` | 6160 kB | 6568 kB | 0.94x |
| 06 | `order_items (order_id)` | 119.6 MB | 64.2 MB | 1.86x |
| 06 | `order_items (order_id) INCLUDE (product_id, quantity)` | 279 MB | 120 MB | 2.32x |

The estimate is always above the real size for B-tree, from 1.2x to 3.4x and it is worst for indexes on a column with many repeated values (`user_id`, `status`). The cause was not investigated. Only `hash` is close. The order of magnitude is right, the number is not usable as is.

## 4. Why not `EXPLAIN ANALYZE`

A hypothetical index exists only in the planner's memory of one connection: it has no file and no pages, so the executor has nothing to read. HypoPG makes plain `EXPLAIN` see it and hides it from `EXPLAIN ANALYZE`, which has to run the query. Checked: with the same hypothetical index `(user_id, created_at DESC)`, `EXPLAIN (ANALYZE, BUFFERS)` shows `Parallel Seq Scan` + `Sort`, 18511 buffers, 80 ms, as without the index. `EXPLAIN (BUFFERS)` without `ANALYZE` has no buffer counts at all.

So HypoPG can give only the planner's **cost**, an estimate in abstract units, never real time or buffers.
