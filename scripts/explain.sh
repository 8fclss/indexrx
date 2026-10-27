#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."

if [ $# -ne 1 ]; then
  echo "usage: $0 eval/cases/<file>.sql" >&2
  exit 1
fi

{ echo "EXPLAIN (ANALYZE, BUFFERS)"; cat "$1"; } | \
  docker compose exec -T postgres \
    sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1'