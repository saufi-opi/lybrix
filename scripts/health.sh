#!/usr/bin/env bash
# scripts/health.sh — quick operator health check (PRD §12).
set -euo pipefail

API_URL="${API_URL:-http://localhost:8000}"

echo "== /v1/system/health =="
curl -fsS "$API_URL/v1/system/health" | python3 -m json.tool

echo
echo "== /v1/system/queues =="
curl -fsS "$API_URL/v1/system/queues" | python3 -m json.tool
