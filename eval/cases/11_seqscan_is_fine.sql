-- Orders of the last year (about half of the table)
SELECT *
FROM orders
WHERE created_at >= now() - interval '365 days';
