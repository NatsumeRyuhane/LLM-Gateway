# Repository governance

All normal changes enter `main` through pull requests containing signed,
Conventional Commits and reproducible automated evidence. `CODEOWNERS` identifies
the responsible maintainer, while the issue and pull-request templates require
scope, failure, observability, security, documentation, and exit evidence before
merge.

## Protected `main` contract

`.github/rulesets/main.json` is the canonical, importable repository ruleset. It
targets the default branch with active enforcement and no bypass actors. It:

- blocks deletion and force pushes;
- requires verified signatures without enforcing linear history;
- requires merge commits so focused, signed commits and the pull-request boundary
  remain visible in repository history;
- requires all review conversations to be resolved;
- requires the stable `repository`, `backend`, `frontend`, `integration`,
  `security`, and `container` checks against the latest base;
- requires zero approvals only while the repository has no independent reviewer.

The required `repository` check validates every pull-request commit subject
against the Conventional Commits contract. This remains a CI policy because the
repository ruleset API does not accept commit-metadata rules for this repository.

Squash and rebase merges are disabled. A change that contains multiple focused
commits must retain those commits; a change that should be atomic should be
authored as one commit before review. Do not trade away reviewed commit structure
at merge time.

The zero-approval setting is an explicit single-maintainer limitation, not a
statement that self-review is sufficient. As soon as a qualified independent
reviewer is available, change `required_approving_review_count` to `1`, set
`require_last_push_approval` to `true`, and apply the updated ruleset in a reviewed
pull request.

## Activation and verification

GitHub only allows a useful required-check selection after each check has reported
at least once. The rollout is therefore:

1. Push the governance branch and open its pull request without the ruleset.
2. Confirm all six job names report on the head commit.
3. From an authenticated administrator session, run
   `bash scripts/configure-main-ruleset.sh OWNER/REPOSITORY HEAD_SHA`.
4. Read back the active rules for `main` and verify there are no bypass actors.
5. Confirm the pull request cannot merge while a check is failing or a conversation
   is unresolved.

The script refuses to create or update protection until every required check has
reported on the evidence commit. It updates a uniquely named existing ruleset or
creates it when absent; duplicate matching rulesets require manual resolution.

Rollback is an administrator action: disable the ruleset only to recover from a
confirmed repository lockout, record the incident, restore the last known-good
JSON, and reactivate protection. Routine changes never use an administrator
bypass because none is configured.
