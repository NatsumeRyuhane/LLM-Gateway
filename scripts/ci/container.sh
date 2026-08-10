#!/usr/bin/env bash
set -euo pipefail

dockerfiles="$(git ls-files | awk '/(^|\/)Dockerfile([^\/]*)$/')"

if [[ -z "$dockerfiles" ]]; then
  echo "::notice::No tracked Dockerfile exists; container builds activate when deployment artifacts land."
  exit 0
fi

while IFS= read -r dockerfile; do
  context="$(dirname "$dockerfile")"
  image_name="llm-gateway-ci-$(printf '%s' "$dockerfile" | tr '/.' '--')"
  docker build --file "$dockerfile" --tag "$image_name" "$context"
done <<< "$dockerfiles"
