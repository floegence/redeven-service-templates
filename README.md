# Redeven Service Templates

This repository is the declarative source of Redeven's built-in Managed Service templates.

It publishes a versioned Go module containing a deterministic, integrity-checked catalog bundle. Service-specific metadata, localized copy, reviewed artifacts, and visual assets belong here. Runtime lifecycle logic and compatibility readers do not.

## Layout

Each template lives at `templates/<template-id>/` with one `template.json`, ten explicit locale files under `locales/`, and reviewed local assets under `assets/`. Compatibility readers and executable lifecycle code are intentionally forbidden.

## Build and verify

```sh
go run ./cmd/build-catalog
go run ./cmd/build-catalog --verify
go test ./...
```

The root Go package exports immutable copies of the catalog bundle and manifest, plus the release version and bundle SHA-256. Consumers must validate the embedded bundle before starting their template catalog or Managed Service runtime.
