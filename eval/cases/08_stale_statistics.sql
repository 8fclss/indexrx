-- Total of the "fresh" orders
SELECT sum(total)
FROM orders
WHERE created_at > now() + interval '1 day';
