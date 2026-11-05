#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s [--quick|--full|--images]\n' "$0" >&2
  exit 2
}

mode=${1:---full}
case "$mode" in
  --quick|--full|--images) ;;
  *) usage ;;
esac

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

require() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'Required tool is missing: %s\n' "$1" >&2
    exit 1
  }
}

require git
require go
require gofmt
require pkg-config
require helm

if ! pkg-config --exists sqlite3; then
  printf 'SQLite development headers and pkg-config metadata are required.\n' >&2
  exit 1
fi

git diff --quiet || {
  printf 'Local verification requires a clean tracked working tree.\n' >&2
  exit 1
}
git diff --cached --quiet || {
  printf 'Local verification requires a clean Git index.\n' >&2
  exit 1
}

mapfile -d '' gofiles < <(git ls-files -z -- '*.go')
if (("${#gofiles[@]}" == 0)); then
  printf 'No tracked Go files found.\n' >&2
  exit 1
fi
unformatted=$(gofmt -l "${gofiles[@]}")
if [[ -n "$unformatted" ]]; then
  printf 'Unformatted Go files:\n%s\n' "$unformatted" >&2
  exit 1
fi

printf 'Verifying source with %s\n' "$(go version)"
go vet ./...
go test -count=1 ./...

if [[ "$mode" != --quick ]]; then
  go test -race -count=1 ./internal/controller ./internal/worker
fi

build_dir=$(mktemp -d)
trap 'rm -rf -- "$build_dir"' EXIT
CGO_ENABLED=1 go build -o "$build_dir/controller" ./cmd/project-workspace-controller
go build -o "$build_dir/worker" ./cmd/project-workspace-worker

helm lint deploy/helm/project-workspace
helm template pwm-system deploy/helm/project-workspace > "$build_dir/default.yaml"
test -s "$build_dir/default.yaml"
helm template pwm-system deploy/helm/project-workspace \
  --set controller.localGit.existingClaimName=pwm-local-git \
  --set controller.localGit.authorName=ReleaseVerifier \
  --set controller.localGit.authorEmail=release-verifier@example.org \
  > "$build_dir/local-git.yaml"
grep -q 'PWM_GIT_LOCAL_ROOT' "$build_dir/local-git.yaml"

if [[ "$mode" == --images ]]; then
  require docker
  docker build -f deploy/kubernetes/Dockerfile.controller \
    -t project-workspace-controller:local-check .
  docker build -f deploy/kubernetes/Dockerfile.worker \
    -t project-workspace-worker:local-check .
fi

printf 'Local source and packaging verification passed.\n'
