#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo 'Usage: package-kubernetes.sh OUTPUT_DIRECTORY' >&2
  exit 2
fi

for tool in git docker helm gzip sha256sum mktemp; do
  command -v "$tool" >/dev/null || { echo "Missing required tool: $tool" >&2; exit 2; }
done

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ ! -f "$root/deploy/helm/project-workspace/Chart.yaml" ]]; then
  echo 'Run the packaging script from its Project Workspace source tree' >&2
  exit 2
fi
if [[ -n "$(git -C "$root" status --porcelain --untracked-files=all)" ]]; then
  echo 'Refusing to package an uncommitted or untracked source tree' >&2
  exit 1
fi
commit="$(git -C "$root" rev-parse --verify HEAD)"
if [[ ! $commit =~ ^[a-f0-9]{40}$ ]]; then
  echo 'Source revision is not a full Git commit SHA' >&2
  exit 1
fi

parent="$(cd "$(dirname "$1")" && pwd -P)"
name="$(basename "$1")"
if [[ $name == '.' || $name == '..' || $name == '' ]]; then
  echo 'Supply a new output directory name' >&2
  exit 2
fi
destination="$parent/$name"
case "$destination" in
  "$root"/*) echo 'Keep release bundles outside the source tree' >&2; exit 2 ;;
esac
if [[ -e $destination ]]; then
  echo 'Release output directory already exists' >&2
  exit 1
fi

umask 077
stage="$(mktemp -d "$parent/.project-workspace-bundle.XXXXXXXX")"
trap 'rm -rf -- "$stage"' EXIT
tag="git-${commit:0:12}"
controller="project-workspace-controller:$tag"
worker="project-workspace-worker:$tag"

docker buildx build --platform linux/amd64 --provenance=false --load \
  -f "$root/deploy/kubernetes/Dockerfile.controller" -t "$controller" "$root"
docker buildx build --platform linux/amd64 --provenance=false --load \
  -f "$root/deploy/kubernetes/Dockerfile.worker" -t "$worker" "$root"
docker save "$controller" "$worker" | gzip -n > "$stage/project-workspace-images-linux-amd64.tar.gz"
git -C "$root" archive --format=tar HEAD | gzip -n > "$stage/project-workspace-source.tar.gz"
helm package "$root/deploy/helm/project-workspace" --destination "$stage" >/dev/null
shopt -s nullglob
charts=("$stage"/project-workspace-*.tgz)
if [[ ${#charts[@]} -ne 1 ]]; then
  echo 'Expected exactly one packaged Helm chart' >&2
  exit 1
fi
{
  printf 'source_commit=%s\n' "$commit"
  printf 'platform=linux/amd64\n'
  printf 'controller_image=%s\n' "$controller"
  printf 'controller_image_id=%s\n' "$(docker image inspect --format '{{.Id}}' "$controller")"
  printf 'worker_image=%s\n' "$worker"
  printf 'worker_image_id=%s\n' "$(docker image inspect --format '{{.Id}}' "$worker")"
} > "$stage/RELEASE.txt"
(
  cd "$stage"
  sha256sum RELEASE.txt project-workspace-source.tar.gz \
    project-workspace-images-linux-amd64.tar.gz "$(basename "${charts[0]}")" > SHA256SUMS
  sha256sum --check --quiet SHA256SUMS
)
mv -- "$stage" "$destination"
trap - EXIT
printf 'Verified release bundle: %s\n' "$destination"
