-- Search for a user by email, ignoring case
SELECT *
FROM users
WHERE lower(email) = 'user12345@example.com';