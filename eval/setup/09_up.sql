CREATE INDEX orders_user_id_idx         ON orders (user_id);
CREATE INDEX orders_user_id_idx2        ON orders (user_id);                          -- exact duplicate
CREATE INDEX orders_user_created_idx    ON orders (user_id, created_at);              -- (user_id) is its prefix
CREATE INDEX orders_user_pending_idx    ON orders (user_id) WHERE status = 'pending'; -- looks like a duplicate, but is partial
CREATE INDEX orders_status_idx          ON orders (status);                           -- unrelated to the others
