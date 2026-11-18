#!/usr/bin/env bash
# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https://www.datadoghq.com/).
# Copyright 2026-present Datadog, Inc.
#
# Print two git tree IDs, "<staged> <working>": the staged state, and the whole working tree with unstaged and
# untracked (not ignored) files included, computed on a copy of the index so the real one is untouched. Two equal
# outputs mean the same staged content and the same files on disk. Exits non-zero, printing nothing on stdout, when
# any git command fails, so a failed snapshot can't compare equal to another one.
set -euo pipefail

index=$(mktemp)
trap 'rm -f "$index"' EXIT
cp "$(git rev-parse --git-path index)" "$index"
staged=$(GIT_INDEX_FILE=$index git write-tree)
GIT_INDEX_FILE=$index git add -A
working=$(GIT_INDEX_FILE=$index git write-tree)
[[ $staged =~ ^[0-9a-f]{40,64}$ && $working =~ ^[0-9a-f]{40,64}$ ]]
echo "$staged $working"
