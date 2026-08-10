#!/usr/bin/env bash
set -euo pipefail

if [[ ! -f scripts/generate.sh ]]; then
  echo "::notice::No generator entrypoint exists; generated-file verification is currently vacuous."
  exit 0
fi

bash scripts/generate.sh

if [[ -n "$(git status --short)" ]]; then
  echo "generated files are not consistent with their sources:" >&2
  git status --short >&2
  git diff -- >&2
  exit 1
fi
