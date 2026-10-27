-- Items for all orders belonging to a single user
SELECT o.id, i.product_id, i.quantity
FROM orders o
JOIN order_items i ON i.order_id = o.id
WHERE o.user_id = 42;