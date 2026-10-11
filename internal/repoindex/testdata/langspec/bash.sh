#!/usr/bin/env bash
set -euo pipefail

total() {
  local sum=0
  for it in "$@"; do
    sum=$((sum + it))
  done
  echo "$sum"
}

function clear_cart {
  if [ -n "${CART:-}" ]; then
    unset CART
  fi
}

total 1 2 3
