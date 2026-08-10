#!/usr/bin/env bash
set -euo pipefail

repository="${1:-NatsumeRyuhane/LLM-Gateway}"
evidence_sha="${2:-$(git rev-parse HEAD)}"
ruleset_path=".github/rulesets/main.json"
ruleset_name="$(jq -r .name "$ruleset_path")"

required_checks="$(
  jq -r '.rules[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context' "$ruleset_path"
)"
observed_checks="$(
  gh api "repos/$repository/commits/$evidence_sha/check-runs" --paginate --jq '.check_runs[].name'
)"

while IFS= read -r required_check; do
  if ! printf '%s\n' "$observed_checks" | grep -Fxq "$required_check"; then
    echo "required check has not reported for $evidence_sha: $required_check" >&2
    exit 1
  fi
done <<< "$required_checks"

ruleset_ids="$(
  gh api "repos/$repository/rulesets" --paginate \
    --jq ".[] | select(.name == \"$ruleset_name\") | .id"
)"
ruleset_count="$(printf '%s\n' "$ruleset_ids" | sed '/^$/d' | wc -l | tr -d ' ')"

case "$ruleset_count" in
  0)
    gh api --method POST "repos/$repository/rulesets" --input "$ruleset_path"
    ;;
  1)
    gh api --method PUT "repos/$repository/rulesets/$ruleset_ids" --input "$ruleset_path"
    ;;
  *)
    echo "multiple rulesets named '$ruleset_name' exist; refusing to choose one" >&2
    exit 1
    ;;
esac
