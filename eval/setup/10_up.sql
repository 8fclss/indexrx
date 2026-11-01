CREATE INDEX orders_user_id_idx ON orders (user_id);
CREATE INDEX orders_total_idx   ON orders (total);
SELECT pg_stat_reset();
