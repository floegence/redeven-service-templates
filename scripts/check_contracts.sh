#!/usr/bin/env bash
set -euo pipefail
export GOWORK=off
go run ./cmd/generate-contracts --verify
go run ./cmd/build-catalog --verify
node --test sdk/*.test.cjs
go test ./...
git diff --exit-code
