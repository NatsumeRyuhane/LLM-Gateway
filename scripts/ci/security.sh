#!/usr/bin/env bash
set -euo pipefail

failure=0

while IFS= read -r path; do
  case "$path" in
    *.env.example|*.env.sample)
      ;;
    *.env|*.env.*)
      echo "tracked environment file is not an approved example: $path" >&2
      failure=1
      ;;
  esac
done < <(git ls-files)

if git grep -nE 'BEGIN (RSA|OPENSSH|EC|DSA) PRI[V]ATE KEY' -- . ':!scripts/ci/security.sh'; then
  echo "private-key material must never be committed" >&2
  failure=1
fi

if git grep -nE '^[[:space:]]*pull_request_target:' -- .github/workflows; then
  echo "pull_request_target requires a separate threat review" >&2
  failure=1
fi

if git grep -nE '^[[:space:]]*permissions:[[:space:]]*write-all' -- .github/workflows; then
  echo "workflows must use least-privilege permissions" >&2
  failure=1
fi

for workflow in .github/workflows/*.yml; do
  grep -q '^permissions:$' "$workflow" || {
    echo "workflow does not declare permissions: $workflow" >&2
    failure=1
  }
done

exit "$failure"
