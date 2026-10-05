#!/bin/sh
# Blocks staged edits to guardrail files. They are owned by the repository
# owner (CODEOWNERS) and are meant to change deliberately, not in passing.
# Bypass a single intended change with: GUARDRAILS_OK=1 git commit ...
set -eu

if [ "${GUARDRAILS_OK:-}" = "1" ]; then
	echo "guardrail check bypassed (GUARDRAILS_OK=1)"
	exit 0
fi

staged=$(git diff --cached --name-only --diff-filter=ACMRD)
hits=$(printf '%s\n' "$staged" | grep -E '^(\.golangci\.yml|lefthook\.yml|\.github/|internal/arch/)' || true)

if [ -n "$hits" ]; then
	echo "staged changes touch guardrail files:"
	printf '%s\n' "$hits"
	echo "if this is intended, re-run with GUARDRAILS_OK=1"
	exit 1
fi
