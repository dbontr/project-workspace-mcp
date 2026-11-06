#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo 'Usage: verify-published-images.sh CONTROLLER_IMAGE@sha256:DIGEST WORKER_IMAGE@sha256:DIGEST' >&2
  exit 2
fi
if ! command -v docker >/dev/null || ! docker buildx version >/dev/null 2>&1; then
  echo 'Docker with Buildx is required' >&2
  exit 2
fi
if [[ "$1" == "$2" ]]; then
  echo 'Controller and worker must reference different images' >&2
  exit 2
fi
for image in "$@"; do
  if [[ ! "$image" =~ ^[a-z0-9._:/-]+@sha256:[a-f0-9]{64}$ ]]; then
    echo "Invalid immutable OCI image reference: $image" >&2
    exit 2
  fi
done
for image in "$@"; do
  docker buildx imagetools inspect "$image" >/dev/null
  docker pull --platform linux/amd64 "$image" >/dev/null
  platform="$(docker image inspect --format '{{.Os}}/{{.Architecture}}' "$image")"
  if [[ "$platform" != 'linux/amd64' ]]; then
    echo "Image has an unsupported platform: $image ($platform)" >&2
    exit 1
  fi
  printf 'Verified retrievable linux/amd64 OCI image: %s\n' "$image"
done
