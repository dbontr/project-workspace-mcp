#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
PREFIX=${PWM_INSTALL_PREFIX:-"$HOME/.local/bin"}
mkdir -p "$PREFIX"
(cd "$ROOT" && CGO_ENABLED=1 go build -trimpath -o "$PREFIX/project-workspace-controller" ./cmd/project-workspace-controller)
(cd "$ROOT" && CGO_ENABLED=1 go build -trimpath -o "$PREFIX/project-workspace-worker" ./cmd/project-workspace-worker)
chmod 700 "$PREFIX/project-workspace-controller" "$PREFIX/project-workspace-worker"
printf 'Installed Go controller and worker into %s\n' "$PREFIX"
