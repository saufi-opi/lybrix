#!/usr/bin/env bash
# scripts/build-anydoc-lib.sh — build the anydoc Rust staticlib for the
# current target triple and install it into third_party/anydoc-go/lib/.
#
# Usage: scripts/build-anydoc-lib.sh <path-to-anydoc-rust-checkout>
#
# The crate must declare crate-type = ["staticlib"] in its Cargo.toml.
set -euo pipefail

SRC_DIR="${1:?usage: build-anydoc-lib.sh <anydoc-rust-checkout>}"
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="${REPO_ROOT}/third_party/anydoc-go"

command -v cargo >/dev/null || { echo "error: cargo not on PATH" >&2; exit 1; }
command -v cbindgen >/dev/null || { echo "error: cbindgen not on PATH (cargo install cbindgen)" >&2; exit 1; }

echo "== cargo build --release in ${SRC_DIR}"
(cd "${SRC_DIR}" && cargo build --release)

TARGET_TRIPLE="$(rustc -vV | awk '/^host:/ {print $2}')"
ARCHIVE="${SRC_DIR}/target/release/libanydoc_go.a"
[ -f "${ARCHIVE}" ] || { echo "error: ${ARCHIVE} not produced" >&2; exit 1; }

OUT_DIR="${DEST}/lib/${TARGET_TRIPLE//-/_}"
mkdir -p "${OUT_DIR}"
cp "${ARCHIVE}" "${OUT_DIR}/libanydoc_go.a"
echo "== installed ${OUT_DIR}/libanydoc_go.a"

echo "== cbindgen -> include/anydoc.h"
cbindgen --crate anydoc_go --output "${DEST}/include/anydoc.h" >/dev/null
echo "== done"
