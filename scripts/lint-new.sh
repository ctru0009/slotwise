#!/bin/sh
# Lints only the lines changed since the last commit so commits stay fast.
# Falls back to HEAD on the first commit of a repo and to a full run when
# there is no commit to diff against yet.
set -eu

if rev=$(git rev-parse --verify -q HEAD^) || rev=$(git rev-parse --verify -q HEAD); then
	exec go tool golangci-lint run --new-from-rev="$rev" ./...
fi

exec go tool golangci-lint run ./...
