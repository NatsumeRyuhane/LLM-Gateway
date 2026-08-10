#!/usr/bin/env bash
set -euo pipefail

base_ref="${1:?base commit is required}"
head_ref="${2:-HEAD}"
pattern='^(feat|fix|docs|refactor|test|build|chore|ci|perf|revert)(\([a-z0-9._/-]+\))?!?: .+'
invalid=0

while IFS= read -r subject; do
  if [[ ! "$subject" =~ $pattern ]]; then
    echo "commit subject is not Conventional Commits compliant: $subject" >&2
    invalid=1
  fi
done < <(git log --format=%s "$base_ref..$head_ref")

exit "$invalid"
