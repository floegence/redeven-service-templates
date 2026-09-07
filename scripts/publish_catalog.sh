#!/usr/bin/env bash
set -euo pipefail

[ "$#" -eq 2 ] || { echo "usage: publish_catalog.sh <base> <private-branch>" >&2; exit 2; }
base="$(git rev-parse --verify "${1}^{commit}")"
branch="$2"
[[ "$branch" == codex/* ]] || { echo "a private codex branch is required" >&2; exit 1; }
test "$(git symbolic-ref HEAD)" = refs/heads/main
test -z "$(git status --porcelain)"
test "$(git rev-parse main)" = "$base"
git merge-base --is-ancestor "$base" "$branch"
version="$(git show "${branch}:catalog.go" | sed -nE 's/^const Version = "(v[0-9]+\.[0-9]+\.[0-9]+)"$/\1/p')"
test -n "$version"
git fetch origin main
test "$(git rev-parse origin/main)" = "$base"
# A failed lookup is an error, not evidence that the tag is available.
remote_tag="$(git ls-remote --refs origin "refs/tags/$version")"
test -z "$remote_tag"
if git show-ref --verify --quiet "refs/tags/$version"; then
  echo "catalog tag already exists locally: $version" >&2
  exit 1
fi
git merge --ff-only "$branch"
git tag -a "$version" -m "Release service template catalog $version"
git push --atomic origin main "refs/tags/$version"
echo "Published $(git rev-parse main) and immutable $version"
