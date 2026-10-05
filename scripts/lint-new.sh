#!/bin/sh
# Lints only the lines changed since the last commit so commits stay fast.
# Falls back to a full lint when there is no previous commit to diff against
# (single-commit repos, shallow clones).
set -eu

if rev=$(git rev-parse --verify -q HEAD^); then
	exec go tool golangci-lint run --new-from-rev="$rev" ./...
fi

exec go tool golangci-lint run ./...
