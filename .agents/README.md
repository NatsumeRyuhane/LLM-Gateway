# Shared agent entrypoint

This directory contains only stable repository-level instructions. `.claude`
points here so Claude and other coding agents receive the same entrypoint.

Do not copy issues or the product design into this directory. They are kept in
live systems deliberately:

- [GitHub Issues](https://github.com/NatsumeRyuhane/LLM-Gateway/issues) are the
  source of truth for task scope, dependencies, acceptance criteria, and status.
- [Adaptive LLM Gateway on Notion](https://app.notion.com/p/3b46d181813e8194bb0bd5ba1c7d73ca)
  is the continuously synchronized source of truth for the large product and
  system design.

## Starting an issue conversation

Tell the agent which GitHub Issue to implement. The agent must then:

1. Read `AGENTS.md`.
2. Fetch the live issue, including relevant comments and linked dependencies.
3. Fetch the live Notion design and read the sections relevant to that issue.
4. Inspect current repository code and checked-in decisions before editing.
5. Work on a dedicated branch and hand off with validation evidence.

Example:

> Implement GitHub Issue #5. Follow `.agents/AGENTS.md`, read the live issue and
> the current Notion design first, and keep the change within the issue scope.

Do not put secrets, provider payloads, user content, local environment files, or
conversation transcripts in this directory.
