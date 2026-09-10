# Redeven Service Templates Repository Guide

- Keep all maintained repository content in English except localized template catalogs.
- Never develop feature changes directly on `main`; use a `codex/` feature branch and a dedicated worktree after the initial repository bootstrap.
- Template definitions are declarative data. Lifecycle hook scripts may be declared as TemplateSpec data and executed only by Redeven. The published template package owns pure, deterministic historical-format readers and forward adapters. Preserve every supported historical input and its semantics without rewriting source files. Do not add lifecycle executors or database migration code.
- Every template must provide complete explicit localization for every locale declared by the bundle manifest.
- Generated bundle artifacts must be deterministic, committed, and verified before release.
- Use Conventional Commit messages with a lowercase type and scope.
- Release tags are immutable. Existing bundle schemas and published template identities must not be rewritten silently.


- `redeven-service-template.json` with `kind: "redeven.service-template"` is the branded current entrypoint. Legacy `template.json` remains a strict historical input only.
- External templates choose their own explicit default locale. The official catalog must still provide every published product locale.
- GitHub source acquisition and file transfer must not execute Git or template code, persist credentials, or fetch application dependencies.
- Historical source fixtures and compatibility tests are a permanent release gate. A new schema must preserve older readers and add explicit, tested data adapters.
- One task owns at most one feature worktree at a time. Keep feature branches private; preserve intentional commits and fast-forward main for publication.
