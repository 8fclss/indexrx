-- "heavy" user's last 20 orders in 90 days
SELECT *
FROM orders
WHERE user_id = 100
  AND created_at >= now() - interval '90 days'
ORDER BY created_at DESC
LIMIT 20;