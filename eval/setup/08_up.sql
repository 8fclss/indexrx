-- Disable autovacuum for orders, otherwise autoanalyze may kick in on its own
ALTER TABLE orders SET (autovacuum_enabled = false);

CREATE INDEX orders_created_at_idx ON orders (created_at);

-- "Nightly load": 500k new orders with dates outside the range of the old statistics.
-- Future dates are a simplification: in a real system growing timestamps fall outside
-- the histogram of the last ANALYZE in the same way.
INSERT INTO orders (id, user_id, status, total, created_at)
SELECT
    2000000 + g,
    1 + floor(power(random(), 3) * 200000)::bigint,
    'completed',
    round((random() * 500 + 5)::numeric, 2),
    now() + random() * interval '5 days'
FROM generate_series(1, 500000) AS g;
