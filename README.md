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

The root Go package exports immutable copies of the catalog bundle and manifest, plus the release version and bundle SHA-256. Consumers must validate the embedded bundle before starting their template catalog or Managed Service runtime.
