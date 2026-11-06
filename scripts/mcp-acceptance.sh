#!/usr/bin/env bash
set -euo pipefail

usage() {
  printf 'Usage: %s MCP_URL BEARER_TOKEN_FILE\n' "$0" >&2
  exit 2
}

[[ "$#" -eq 2 ]] || usage
url=${1%/}
token_file=$2

case "$url" in
  https://*|http://127.0.0.1:*|http://localhost:*) ;;
  *) printf 'MCP_URL must use HTTPS or a loopback HTTP address.\n' >&2; exit 2 ;;
esac
case "$url" in
  *'@'*) printf 'Credentials must not be embedded in MCP_URL.\n' >&2; exit 2 ;;
esac
[[ -f "$token_file" && -r "$token_file" ]] || {
  printf 'Bearer token file is not readable.\n' >&2
  exit 2
}
command -v curl >/dev/null || { printf 'curl is required.\n' >&2; exit 2; }
command -v jq >/dev/null || { printf 'jq is required.\n' >&2; exit 2; }

token=$(<"$token_file")
[[ -n "$token" && "$token" != *$'\n'* && "$token" != *$'\r'* ]] || {
  printf 'Bearer token file contains an invalid token.\n' >&2
  exit 2
}

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"; unset token' EXIT
chmod 700 "$scratch"

rpc() {
  local data=$1
  local path=$2
  local authenticated=$3
  local status
  local -a arguments=(
    --silent --show-error --max-time 20
    --header 'Content-Type: application/json'
    --header 'Accept: application/json, text/event-stream'
  )
  if [[ "$authenticated" == yes ]]; then
    arguments+=(--header "Authorization: Bearer $token")
  fi
  status=$(printf '%s' "$data" | curl "${arguments[@]}" --data-binary @- --output "$scratch/$path" --write-out '%{http_code}' "$url")
  printf '%s' "$status"
}

initialize='{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"mcp-acceptance","version":"1.0"}}}'
unauthorized=$(rpc "$initialize" unauthorized.json no)
[[ "$unauthorized" == 401 ]] || {
  printf 'FAIL: unauthenticated initialize returned HTTP %s, not 401.\n' "$unauthorized" >&2
  exit 1
}

status=$(rpc "$initialize" initialized.json yes)
[[ "$status" == 200 ]] && jq -e '
  .jsonrpc == "2.0"
  and .id == 1
  and .result.protocolVersion == "2025-06-18"
  and .result.serverInfo.name == "workspace-mcp"
' "$scratch/initialized.json" >/dev/null || {
  printf 'FAIL: authenticated initialization was rejected or malformed (HTTP %s).\n' "$status" >&2
  exit 1
}

status=$(rpc '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' tools.json yes)
[[ "$status" == 200 ]] && jq -e '
  (.result.tools | type) == "array"
  and (.result.tools | length) >= 10
  and all(.result.tools[]; (.name | type) == "string" and (.inputSchema | type) == "object")
  and ([.result.tools[].name] | unique | length) == (.result.tools | length)
  and ([.result.tools[].name] | index("list_projects") != null)
  and ([.result.tools[].name] | index("git_provider_capabilities") != null)
' "$scratch/tools.json" >/dev/null || {
  printf 'FAIL: authenticated tool discovery was rejected or malformed (HTTP %s).\n' "$status" >&2
  exit 1
}

status=$(rpc '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"git_provider_capabilities","arguments":{"provider":"local-git"}}}' provider.json yes)
[[ "$status" == 200 ]] && jq -e '
  .result.isError == false
  and .result.structuredContent.provider == "local-git"
  and .result.structuredContent.issues == "unavailable"
' "$scratch/provider.json" >/dev/null || {
  printf 'FAIL: local Git capability negotiation was rejected or malformed (HTTP %s).\n' "$status" >&2
  exit 1
}

status=$(rpc '{"jsonrpc":"2.0","id":4,"method":"ping","params":{}}' ping.json yes)
[[ "$status" == 200 ]] && jq -e '.result == {} and .id == 4' "$scratch/ping.json" >/dev/null || {
  printf 'FAIL: authenticated MCP ping was rejected (HTTP %s).\n' "$status" >&2
  exit 1
}

printf 'PASS: unauthenticated requests denied; authenticated initialize, tools/list, provider capabilities and ping verified.\n'
printf 'Discovered tools: %s\n' "$(jq -r '.result.tools | length' "$scratch/tools.json")"
