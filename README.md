# Redeven Service Templates

The public Redeven Service Template standard, historical readers, GitHub source
acquisition SDKs, and official catalog live in this repository. External GitHub
repositories use the same data contract as official templates. Redeven owns
permissions, installation, service execution, and database migrations.

## Author a template

Place `redeven-service-template.json` in the repository root or in a directory
under `templates/`. A directory is one template, including its local assets and
helper scripts. Start with `examples/minimal-host/`.

```json
{
  "kind": "redeven.service-template",
  "schema_version": 3,
  "template_id": "example-web",
  "service_family_id": "example-web",
  "revision": 1,
  "default_locale": "en-US",
  "locales": ["en-US"],
  "spec": {
    "schema_version": 6,
    "kind": "host",
    "endpoint": {"scheme": "http"},
    "host": {"start_script": "exec example-server --port \"$REDEVEN_SERVICE_PORT\""}
  }
}
```

Each declared locale needs a complete `locales/<locale>.json` containing `name`,
`description`, and every declared notice. Unmatched languages use the template's
own `default_locale`. The official catalog additionally requires all ten product
locales. Icons are local, reviewed SVG files. Scripts can reference helper files
through `REDEVEN_TEMPLATE_DIR`; acquisition and validation never execute them.
The service workspace, launch directory, parameters, ports, process ownership,
and permission boundaries remain host-owned.

`kind` is fixed. `schema_version` identifies the source document format;
`spec.schema_version` independently identifies execution data; `revision` is the
author's template revision. An application release and a Git commit are separate
identities. Increase `revision` when publishing intentional template changes.
A `recommended_version` is an installation default, never automatic tracking.

## Validate and consume

```sh
go run github.com/floegence/redeven-service-templates/cmd/validate-template@v0.6.0 --current ./my-template
```

Without `--current`, the validator also accepts supported historical sources.
Its JSON result reports source and effective versions, default locale, and the
whole-directory SHA-256. Runtime validation is authoritative: schema-valid data
must still satisfy asset, semantic, platform, and permission rules.

The Go `template` package exports the current typed execution specification,
`Read`, `ReadDirectory`, `NormalizeSpec`, `GitHubClient`, and `Snapshot`. The
npm-format `@floegence/redeven-service-templates` SDK is published as an immutable
GitHub Release tarball with generated TypeScript declarations and JSON Schemas.
It provides Desktop acquisition and the same directory transfer format and
digest. The SDK has no package dependencies. Install the release asset with a
lockfile; never wire sibling source repositories into product builds.

## Compatibility contract

| Original entrypoint | Document | Execution input | Effective versions |
| --- | --- | --- | --- |
| `template.json` | 1 | 3 | document 3 / execution 6 |
| `template.json` | 2 | 4–6 | document 3 / execution 6 |
| `redeven-service-template.json` | 3 | 6 | document 3 / execution 6 |

Historical readers strictly identify the original structure before applying
contiguous, deterministic data adapters. Document 1's release default becomes
`recommended_version`; retired source tag filters remain retired as specified
by the published document 2 / execution 4 contract. Execution 4 to 5 adds no
implicit opening behavior. Execution 5's startup-output opening declaration
becomes the equivalent private-output after-start and open hooks in execution 6.
The literal prefix remains data, including quotes and shell metacharacters.
Existing parameter, health, lifecycle, resource, and permission data survives.
Older source documents retain their historical `en-US` localization fallback.

Compatibility is in memory only. Never rewrite originals, store a second
normalized execution definition, or trigger source updates, app upgrades,
reinstallation, or service restarts merely to adapt a format. There is one
current executor. Older formats cannot grant new privileges. Unknown future
versions, malformed historical fields, and simultaneous entrypoints fail clearly
without altering source files. The current document cannot use the legacy name.

Every supported historical format is a permanent compatibility commitment.
Frozen released input bytes and their checksums under `template/testdata/history`
remain in the release gate. New formats retain old schemas/readers and add
explicit adaptation steps and behavioral tests. Pure data adapters belong here;
service execution and database lineage migration do not.

## GitHub source contract

Both SDKs resolve repository, ref, and directory to an exact commit through the
GitHub HTTPS API. They do not use `git`, download Release assets, install service
packages, or execute scripts. Repository, directory, and declaration links work;
an omitted ref uses GitHub's actual default branch. Root templates and immediate
`templates/` children can be discovered for selection. Public and private
repositories use the same API; a private token needs repository Contents read
access and is ephemeral. It is sent only to `api.github.com`, with redirects
rejected, and never appears in snapshots or safe error messages.

Desktop acquires a snapshot locally and transfers it over the authenticated
Runtime connection. Alternatively, Runtime acquires it directly using a token
provided for that request. Both paths validate the same original bytes, portable
relative paths, executable modes, and digest. A snapshot is source data, not proof
that GitHub approved the contents: Runtime always revalidates it before use.

Published limits: 1,024 files, 2 MiB per file, 16 MiB in total, 1,024 UTF-8 bytes
per relative path, 32 directory levels, and 100 discovery candidates. Reject
symlinks, submodules, Git LFS pointers, case-insensitive path collisions,
file/directory collisions, traversal, and incomplete API results. Retain only
regular Git modes `100644` and `100755`. No `.git` history is needed.

Directory SHA-256 hashes UTF-8 byte-sorted records of
`path + NUL + mode + NUL + lowercase-SHA256(file-bytes) + LF`. All files count,
including localizations, icons, and scripts. Changes outside the selected
directory do not change this identity. Registry metadata lives outside originals.
Hosts namespace imported identities by repository, save the ref/path/commit,
and switch complete verified directories atomically after explicit user review.
They never check or update external templates in the background.

## Build and publish

```sh
go run ./cmd/generate-contracts
go run ./cmd/build-catalog --check-registry
go run ./cmd/build-catalog --verify
go run ./cmd/generate-contracts --verify
go test ./...
node --test sdk/*.test.cjs
```

The official compiled catalog retains its existing bundle schema 2 for existing
consumers; source document 3 is independently versioned. Generation validates
sources through the public reader and adds official catalog policy. The OCI
preflight verifies each exact recommendation and architecture digest against its
registry before publication. Generated outputs are deterministic and committed.

The daily/manual reviewed-release watcher checks official npm and OCI upstream
releases, validates candidates, then atomically fast-forwards main and publishes
an immutable Go module tag through `scripts/publish_catalog.sh`. It never moves
a tag or upgrades installed services. Contract and historical tests are required
for both manual and automated publication. The acquisition SDK has an independent
package version; catalog-only patches need not republish unchanged SDK bytes.
