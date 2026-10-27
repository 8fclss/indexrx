-- Events from page /p/123 (about 0.1% of the table)
SELECT count(*)
FROM events
WHERE payload @> '{"page": "/p/123"}';