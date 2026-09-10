#!/usr/bin/env bash
set -euo pipefail

# Hermetic control-flow test: no registry or GitHub connection is possible.
gh() { printf '%s\n' "$TEST_REF_SHA"; }
git() { printf '%s\n' "$TEST_TAG_OBJECT"; }
docker() {
  [[ "$1 $2 $3" == 'buildx imagetools create' ]] || return 90
  [[ "$*" == *"$IMAGE@$DIGEST"* ]] || return 91
  echo 'promoted verified digest'
}
export -f gh git docker
export GITHUB_REPOSITORY=d0linger/Treckrr GITHUB_REF=refs/heads/main GITHUB_SHA=abc
export TEST_REF_SHA=abc TEST_TAG_OBJECT=tagobject
export IMAGE="ghcr.io/${GITHUB_REPOSITORY,,}"
export DIGEST=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
export RELEASE_TAGS=ghcr.io/d0linger/treckrr:latest
GITHUB_STEP_SUMMARY=$(mktemp)
export GITHUB_STEP_SUMMARY
trap 'rm -f "$GITHUB_STEP_SUMMARY"' EXIT
script=$(dirname "$0")/promote-image.sh

bash "$script" | grep -q 'promoted verified digest'
if TEST_REF_SHA=newer bash "$script"; then echo 'accepted superseded branch'; exit 1; fi
if RELEASE_TAGS=ghcr.io/another/repo:latest bash "$script"; then echo 'accepted foreign tag'; exit 1; fi
if DIGEST=invalid bash "$script"; then echo 'accepted invalid digest'; exit 1; fi
GITHUB_REF=refs/tags/v1.2.3 TEST_REF_SHA=tagobject bash "$script" | grep -q 'promoted verified digest'
echo 'Promotion guard tests passed (mock GitHub/registry only).'
