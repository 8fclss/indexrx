-- Orders of users from a single city
SELECT count(*)
FROM users u
JOIN orders o ON o.user_id = u.id
WHERE u.country = 'DE' AND u.city = 'DE-3';
