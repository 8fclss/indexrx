\timing on

TRUNCATE users, orders, order_items, events;

-- Users: 200 000
INSERT INTO users (id, email, country, city, created_at)
SELECT
    g,
    'user' || g || '@example.com',
    c,
    c || '-' || (1 + floor(random() * 10)::int),
    now() - (random() * interval '1000 days')
FROM (
    SELECT g,
           (ARRAY['US','DE','FR','GB','IN','BR','JP','RU','CN','CA'])[1 + floor(random() * 10)::int] AS c
    FROM generate_series(1, 200000) AS g
) s;

-- Orders: 2 000 000, random()^3 -> lower id = more orders
-- 90% completed, 0.5% pending
INSERT INTO orders (id, user_id, status, total, created_at)
SELECT
    g,
    1 + floor(power(random(), 3) * 200000)::bigint,
    CASE
        WHEN r < 0.90  THEN 'completed'
        WHEN r < 0.97  THEN 'shipped'
        WHEN r < 0.995 THEN 'cancelled'
        ELSE 'pending'
    END,
    round((random() * 500 + 5)::numeric, 2),
    now() - (random() * interval '730 days')
FROM (SELECT g, random() AS r FROM generate_series(1, 2000000) AS g) s;

-- Order items: 4 000 000 (2 for each order on average)
INSERT INTO order_items (id, order_id, product_id, quantity, price)
SELECT
    g,
    1 + floor(random() * 2000000)::bigint,
    1 + floor(random() * 5000)::int,
    1 + floor(random() * 5)::int,
    round((random() * 200 + 1)::numeric, 2)
FROM generate_series(1, 4000000) AS g;

-- Events: 3 000 000 with jsonb
INSERT INTO events (id, user_id, type, payload, created_at)
SELECT
    g,
    1 + floor(power(random(), 2) * 200000)::bigint,
    (ARRAY['login','view','click','purchase','logout'])[1 + floor(random() * 5)::int],
    jsonb_build_object(
        'device',  (ARRAY['ios','android','web'])[1 + floor(random() * 3)::int],
        'page',    '/p/' || floor(random() * 1000)::int,
        'ab_test', (random() < 0.1)
    ),
    now() - (random() * interval '365 days')
FROM generate_series(1, 3000000) AS g;

ANALYZE;