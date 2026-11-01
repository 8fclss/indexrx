DELETE FROM orders WHERE id > 2000000;
DROP INDEX orders_created_at_idx;
ALTER TABLE orders RESET (autovacuum_enabled);
VACUUM ANALYZE orders;
