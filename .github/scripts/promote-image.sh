#!/usr/bin/env bash
set -euo pipefail

# A slow rerun must not roll a branch's mutable aliases back after a newer build.
# The job-level concurrency group serializes this check plus promotion.
ref_sha=$(gh api "repos/$GITHUB_REPOSITORY/git/ref/${GITHUB_REF#refs/}" --jq '.object.sha')
source_sha=$GITHUB_SHA
if [[ "$GITHUB_REF" == refs/tags/* ]]; then
  # Annotated tags resolve to a tag object; compare against checkout's tag ref.
  source_sha=$(git rev-parse "$GITHUB_REF")
fi
if [[ "$ref_sha" != "$source_sha" ]]; then
  echo "Ref advanced; refusing to promote this superseded run."
  exit 1
fi
[[ "$DIGEST" =~ ^sha256:[a-f0-9]{64}$ ]] || { echo "Invalid candidate digest"; exit 1; }
tags=()
while IFS= read -r tag; do
  [[ -z "$tag" ]] && continue
  [[ "$tag" == "$IMAGE:"* ]] || { echo "Unexpected release tag repository"; exit 1; }
  tags+=(--tag "$tag")
done <<< "$RELEASE_TAGS"
(( ${#tags[@]} > 0 )) || { echo "No release tags"; exit 1; }
docker buildx imagetools create "${tags[@]}" "$IMAGE@$DIGEST"
{
  echo '### Verified release image'
  echo
  echo "Pin deployments to: \`$IMAGE@$DIGEST\`"
  echo
  echo 'Both linux/amd64 and linux/arm64 passed the candidate vulnerability gate.'
} >> "$GITHUB_STEP_SUMMARY"
