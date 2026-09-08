#!/usr/bin/env sh
# deploy/minio/init.sh — bucket creation (PRD §12 deploy/minio/init.sh).
# Runs once via the mc client against a fresh MinIO.
set -eu

until mc alias set local "${S3_ENDPOINT:-http://minio:9000}" "${MINIO_ROOT_USER}" "${MINIO_ROOT_PASSWORD}" >/dev/null 2>&1; do
  echo "waiting for minio..."
  sleep 2
done

mc mb --ignore-existing local/raw
mc mb --ignore-existing local/parsed

# Parsers get read on raw + write on parsed; the API gets presign only.
mc anonymous set none local/raw
mc anonymous set none local/parsed
echo "buckets ready: raw, parsed"
