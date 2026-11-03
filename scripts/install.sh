#!/bin/sh
set -eu
PYTHON=${PYTHON:-python3}
INSTALL_DIR=${PWM_INSTALL_DIR:-"$HOME/.local/share/project-workspace/venv"}
"$PYTHON" -m venv "$INSTALL_DIR"
"$INSTALL_DIR/bin/python" -m pip install "$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
mkdir -p "$HOME/.local/bin"
ln -sfn "$INSTALL_DIR/bin/project-workspace" "$HOME/.local/bin/project-workspace"
printf 'Installed Project Workspace MCP. Ensure %s is on PATH.\n' "$HOME/.local/bin"
