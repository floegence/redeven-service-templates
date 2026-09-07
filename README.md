# Redeven Service Templates

This repository is the declarative source of Redeven's built-in Managed Service templates.

It publishes a versioned Go module containing a deterministic, integrity-checked catalog bundle. Service-specific metadata, localized copy, recommended default releases, reviewed artifacts, and visual assets belong here. Runtime lifecycle logic and compatibility readers do not.

Each built-in template declares one exact `recommended_version`. Redeven installs that release only when the user does not choose another source release. It is a recommendation and default, never an allowlist, minimum version, or automatic tracking policy.

## Layout

Each template lives at `templates/<template-id>/` with one `template.json`, ten explicit locale files under `locales/`, and reviewed local assets under `assets/`. Compatibility readers and executable lifecycle code are intentionally forbidden.

## Build and verify

```sh
go run ./cmd/build-catalog --check-registry
go run ./cmd/build-catalog --verify
go test ./...
```

`check-registry` is the publish-time OCI preflight. It resolves each OCI
template's recommended tag, requires a manifest index, verifies the Linux
amd64 and arm64 descriptors and their exact declared digests, and fails with a
stable reason for missing, unauthorized, rate-limited, malformed, timed-out,
or unavailable Registry responses. Run it before generating or publishing a
catalog bundle; it does not rewrite template files or generated artifacts.

## Reviewed release watcher

`release_sources.json` is the declarative source map for the scheduled release
watcher. Run `go run ./cmd/release-watcher --check` to inspect the currently
verified upstream releases without changing the checkout. Running it without
`--check` updates only the reviewed catalog inputs after both sources pass
validation: the official npm `latest` dist-tag for Host and the canonical
SemVer GHCR tag (excluding `-market.*` aliases) for the community Container
image. The watcher records exact platform digests and never follows a floating
image tag or silently upgrades an installed service.

The GitHub Actions workflow runs this command daily and on manual dispatch. It
stages changes in a temporary automation branch, runs the catalog generator,
OCI preflight, and all Go tests, then atomically publishes the new `main` tip
and immutable catalog tag. Any upstream, validation, race, or tag collision
failure leaves `main` and generated artifacts unchanged.

The root Go package exports immutable copies of the catalog bundle and manifest, plus the release version and bundle SHA-256. Consumers must validate the embedded bundle before starting their template catalog or Managed Service runtime.
