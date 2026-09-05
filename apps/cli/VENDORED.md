# Vendored terminal UI

`src/cc` contains the subset of terminal UI sources synchronized from a pinned
Claude Code source checkout. Run `pnpm sync:claude-code` with
`CLAUDE_CODE_SRC` pointing at that checkout. The script records its Git commit
in `src/cc/UPSTREAM_REVISION`; review that revision's license and notices before
redistributing an updated bundle.

Kepler-specific behavior belongs outside `src/cc` or in an explicit, reviewable
adapter/stub. Do not copy unrelated source directories or retain backup copies.
