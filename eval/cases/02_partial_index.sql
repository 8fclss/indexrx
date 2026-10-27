-- Unprocessed orders (about 0,5% of the table)
SELECT id, user_id, total
FROM orders
WHERE status = 'pending';