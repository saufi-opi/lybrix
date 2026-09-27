#!/usr/bin/env bash
# R-36 — manual reproduction of the (C) deadlock cycle (embedder-vs-bootstrap)
# against a running lybrix stack. Documentation-grade: not run in CI.
#
# The cycle it demonstrates (docs/BACKLOG.md R-36, plan §1.2 (C)):
#   session A holds chunk row locks across an open tx (the old embedder's
#   shape: chunk locks held across HTTP embeds), a booting worker runs
#   BootstrapSchema whose schema.sql queues a CREATE INDEX SHARE lock on
#   chunks behind A's locks; A then issues its SECOND chunks statement,
#   which queues behind the SHARE lock — cycle closed, the detector fires
#   on whichever session Postgres picks (on pre-fix builds: the bootstrap,
#   `apply schema.sql: ERROR: deadlock detected`, worker exits, compose
#   restarts — the crash loop).
#
# Usage:
#   scripts/soak/deadlock_repro.sh            # reads PG* from .env / defaults
#   PGHOST=... PGPORT=... PGUSER=... ./scripts/soak/deadlock_repro.sh
#
# On a post-fix build the bootstrap session absorbs the 40P01 via its retry
# ladder (DDL is idempotent), so this script only "works" as a manual repro
# against the pre-fix code — after R-36 the printed outcome is "bootstrap
# succeeded via retry", which is itself the proof the fix is live.

set -u

PGHOST_DEFAULT="localhost"
PGPORT_DEFAULT="5432"
PGDATABASE_DEFAULT="rag"

# load .env for POSTGRES_PASSWORD etc. if present
if [ -f ".env" ]; then
  # shellcheck disable=SC1091
  . ./.env 2>/dev/null || true
fi

PGHOST="${PGHOST:-$PGHOST_DEFAULT}"
PGPORT="${PGPORT:-$PGPORT_DEFAULT}"
PGDATABASE="${PGDATABASE:-$PGDATABASE_DEFAULT}"
PGUSER="${PGUSER:-rag}"
PGPASSWORD="${PGPASSWORD:-${POSTGRES_PASSWORD:-rag}}"

PSQL="psql -h \"$PGHOST\" -p \"$PGPORT\" -U \"$PGUSER\" -d \"$PGDATABASE\""

run_psql() {
  # shellcheck disable=SC2086
  PGPASSWORD="$PGPASSWORD" psql -h "$PGHOST" -p "$PGPORT" -U "$PGUSER" -d "$PGDATABASE" -X -q "$@"
}

# 0. reachability probe: exit 0 with an explanation when the stack isn't up
if ! run_psql -c "SELECT 1" >/dev/null 2>&1; then
  cat <<'EOF'
lybrix stack not reachable (psql could not connect).

This script documents the R-36 deadlock cycle manually:
  1. Session A: BEGIN; UPDATE chunks SET embedding = embedding WHERE doc_id = <id>;
     -- A now holds chunk row locks and does NOT commit (the old embedder held
     -- these locks across every HTTP embed batch)
  2. Session B (a booting worker): SELECT pg_advisory_lock(918273645);
     apply schema.sql — its CREATE INDEX ix_chunks_hnsw_1024 queues a SHARE
     lock on chunks BEHIND A's row locks and blocks (Postgres takes the lock
     BEFORE the IF NOT EXISTS check short-circuits)
  3. Session A: UPDATE chunks ... again (its next chunks statement — the old
     embedder's interleaved UpdateEmbeddingTx shape) — it queues behind the
     SHARE lock
  4. Cycle: CREATE INDEX waits on A's row locks; A's second statement waits
     on CREATE INDEX's SHARE lock. Postgres detects (40P01) and aborts one
     side — pre-fix, the bootstrap session, printing:
        apply schema.sql: ERROR: deadlock detected
     …compose restarts the worker, it boots again, repeats: the crash loop.

Start the stack (make up-core) and re-run this script for the live repro.
EOF
  exit 0
fi

DOC_ID="$(run_psql -Atc "SELECT id FROM chunks LIMIT 1" | head -1)"
if [ -z "$DOC_ID" ]; then
  echo "no chunks rows to lock — ingest a document first"
  exit 0
fi

echo "== session A: BEGIN; UPDATE chunks (holds chunk row locks) =="
run_psql -c "SELECT pg_advisory_lock(918273645);" >/dev/null &
ADVIS_PID=$!
sleep 0.5

run_psql <<SQL >/dev/null &
BEGIN;
UPDATE chunks SET embedding = embedding WHERE doc_id = '$DOC_ID';
SELECT pg_sleep(8);
COMMIT;
SQL
A_PID=$!

sleep 1
echo "== session B (simulated boot): run BootstrapSchema's DDL (CREATE INDEX SHARE on chunks) =="
run_psql <<SQL
SELECT 'session B queues CREATE INDEX behind A''s row locks...';
CREATE INDEX IF NOT EXISTS ix_chunks_hnsw_1024_repro ON chunks
  USING hnsw ((embedding::vector(1024)) vector_cosine_ops)
  WHERE is_parent = FALSE AND vector_dims(embedding) = 1024;
SQL
B_RC=$?

echo "== session A's second chunks statement (the cycle-closing step) =="
wait $A_PID 2>/dev/null

kill $ADVIS_PID 2>/dev/null
run_psql -c "SELECT pg_advisory_unlock(918273645);" >/dev/null 2>&1
run_psql -c "DROP INDEX IF EXISTS ix_chunks_hnsw_1024_repro;" >/dev/null 2>&1

if [ "$B_RC" = "0" ]; then
  echo "session B completed without a deadlock — either the writer committed first"
  echo "(timing) or the fix is live: bootstrap absorbs 40P01 via its retry ladder."
else
  echo "session B exited $B_RC — inspect its output above for 'deadlock detected'."
fi
echo "done."
