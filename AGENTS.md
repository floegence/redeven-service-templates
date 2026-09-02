# Redeven Service Templates Repository Guide

- Keep all maintained repository content in English except localized template catalogs.
- Never develop feature changes directly on `main`; use a `codex/` feature branch and a dedicated worktree after the initial repository bootstrap.
- Template definitions are declarative data. Do not add service lifecycle executors, migration code, shell hooks, or compatibility readers.
- Every template must provide complete explicit localization for every locale declared by the bundle manifest.
- Generated bundle artifacts must be deterministic, committed, and verified before release.
- Use Conventional Commit messages with a lowercase type and scope.
- Release tags are immutable. Existing bundle schemas and published template identities must not be rewritten silently.

