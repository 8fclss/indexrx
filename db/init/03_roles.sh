#!/usr/bin/env bash
set -euo pipefail

psql -v ON_ERROR_STOP=1 \
     --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
     -v agent_password="$AGENT_PASSWORD" \
     -v db="$POSTGRES_DB" <<'EOSQL'

CREATE ROLE indexrx_agent
    LOGIN PASSWORD :'agent_password'
    NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION
    CONNECTION LIMIT 5;

-- Default settings for each new role session
ALTER ROLE indexrx_agent SET default_transaction_read_only = on;
ALTER ROLE indexrx_agent SET statement_timeout = '5s';
ALTER ROLE indexrx_agent SET lock_timeout = '1s';
ALTER ROLE indexrx_agent SET idle_in_transaction_session_timeout = '30s';

-- Permissions: connecting, reading tables, system statistics
REVOKE ALL ON DATABASE :"db" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"db" TO indexrx_agent;
GRANT USAGE ON SCHEMA public TO indexrx_agent;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO indexrx_agent;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO indexrx_agent;
GRANT pg_read_all_stats TO indexrx_agent;

EOSQL
