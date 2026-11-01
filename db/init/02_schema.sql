CREATE TABLE users (
    id          bigint PRIMARY KEY,
    email       text        NOT NULL,
    country     text        NOT NULL,
    city        text        NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE TABLE orders (
    id          bigint PRIMARY KEY,
    user_id     bigint      NOT NULL,
    status      text        NOT NULL,
    total       numeric(12,2) NOT NULL,
    created_at  timestamptz NOT NULL
);

CREATE TABLE order_items (
    id          bigint PRIMARY KEY,
    order_id    bigint      NOT NULL,
    product_id  integer     NOT NULL,
    quantity    integer     NOT NULL,
    price       numeric(10,2) NOT NULL
);

CREATE TABLE events (
    id          bigint PRIMARY KEY,
    user_id     bigint      NOT NULL,
    type        text        NOT NULL,
    payload     jsonb       NOT NULL,
    created_at  timestamptz NOT NULL
);