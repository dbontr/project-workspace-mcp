#!/usr/bin/env bash
set -euo pipefail
umask 077

# An operator-managed reconciler grants each code project an isolated
# namespace and worker identity on each approved Kubernetes compute host.
# The controller retains scheduling and project authorization authority.
: "${PWM_WORKER_IMAGE:?a preloaded or registry-accessible worker image is required}"
: "${PWM_HELM_CHART:?the Workspace MCP Helm chart path is required}"
hosts=(${PWM_COMPUTE_NODES:-jupiter venus mercury janus})
exclude=" ${PWM_EXCLUDE_PROJECTS:-acceptance} "
system_namespace="${PWM_SYSTEM_NAMESPACE:-pwm-system}"
helm="${PWM_HELM_BINARY:-helm}"
for command in kubectl curl jq sha256sum base64; do
  command -v "$command" >/dev/null || { echo "missing required command: $command" >&2; exit 1; }
done
command -v "$helm" >/dev/null || { echo "Helm executable unavailable: $helm" >&2; exit 1; }
test -d "$PWM_HELM_CHART" || { echo "Helm chart unavailable" >&2; exit 1; }

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
chmod 700 "$scratch"
fqdn="pwm-controller.$system_namespace.svc.cluster.local"
ip=$(kubectl -n "$system_namespace" get svc pwm-controller -o jsonpath='{.spec.clusterIP}')
test -n "$ip" || { echo "controller service has no ClusterIP" >&2; exit 1; }
kubectl -n "$system_namespace" get secret pwm-controller-ca -o 'jsonpath={.data.ca\.crt}' |
  base64 -d > "$scratch/controller-ca.pem"
admin=$(kubectl -n "$system_namespace" get secret pwm-controller-credentials \
  -o jsonpath='{.data.admin_token}' | base64 -d)
test "${#admin}" -ge 32 || { echo "controller administrator credential unavailable" >&2; exit 1; }

api() {
  local method="$1" path="$2" body="${3:-}"
  local args=(--fail-with-body --silent --show-error --max-time 35
    --cacert "$scratch/controller-ca.pem" --resolve "$fqdn:8443:$ip"
    -H 'Content-Type: application/json' -X "$method")
  if [[ -n "$body" ]]; then args+=(--data-binary "@$body"); fi
  # Keep the administrator credential out of process arguments and logs.
  printf 'header = "Authorization: Bearer %s"\n' "$admin" |
    curl --config - "${args[@]}" "https://$fqdn:8443$path"
}

printf '{}' > "$scratch/empty.json"
api GET /api/projects > "$scratch/projects.json"
api GET /api/nodes > "$scratch/nodes.json"
jq -e 'type == "array"' "$scratch/projects.json" >/dev/null
jq -e 'type == "array"' "$scratch/nodes.json" >/dev/null

while IFS= read -r project; do
  if [[ "$exclude" == *" $project "* ]]; then continue; fi
  if [[ ! "$project" =~ ^[a-z0-9]([-a-z0-9]{0,50}[a-z0-9])?$ ]]; then
    printf 'Project %q requires a DNS-compatible Kubernetes identifier of at most 52 characters\n' "$project" >&2
    continue
  fi
  key=$(printf '%s' "$project" | sha256sum | cut -c 1-12)
  for host in "${hosts[@]}"; do
    if [[ ! "$host" =~ ^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$ ]]; then
      echo "Invalid approved Kubernetes host $host" >&2
      exit 1
    fi
    if jq -e --arg project "$project" --arg host "$host" \
      '.[] | select(.capabilities.kubernetes == true and .capabilities.project_id == $project and .capabilities.workload_node == $host)' \
      "$scratch/nodes.json" >/dev/null; then
      continue
    fi
    node="worker-$key-$host"
    instance="p-$key-$host"
    namespace="pwm-$key-$host"
    secret="pwm-worker-$key-$host-credentials"
    release="pwm-project-$key-$host"
    if ! kubectl -n "$system_namespace" get secret "$secret" >/dev/null 2>&1; then
      api POST /api/pairing "$scratch/empty.json" > "$scratch/pairing.json"
      code=$(jq -er '.pairing_code' "$scratch/pairing.json")
      jq -nc --arg code "$code" --arg node "$node" --arg project "$project" \
        --arg namespace "$namespace" --arg host "$host" \
        '{name:$node,pairing_code:$code,capabilities:{arch:"amd64",cpu:2,memory_mb:4096,max_jobs:1,docker:false,kubernetes:true,os:"linux",images:["debian:bookworm-slim"],devices:[],project_id:$project,namespace:$namespace,workload_node:$host}}' \
        > "$scratch/enrollment.json"
      api POST /api/pair "$scratch/enrollment.json" |
        jq -er '.worker_token' > "$scratch/worker_token"
      chmod 600 "$scratch/worker_token"
      kubectl -n "$system_namespace" create secret generic "$secret" \
        --from-file=worker_token="$scratch/worker_token" >/dev/null
      rm -f "$scratch/worker_token"
    fi
    "$helm" upgrade --install "$release" "$PWM_HELM_CHART" \
      --namespace "$system_namespace" --set controller.enabled=false \
      --set worker.enabled=true --set "worker.name=$node" \
      --set "worker.projectID=$project" --set "worker.instanceID=$instance" \
      --set "worker.workloadNamespace=$namespace" \
      --set "worker.workloadNode=$host" --set "worker.tokenSecretName=$secret" \
      --set-json "worker.nodeSelector={\"kubernetes.io/hostname\":\"$host\"}" \
      --set "worker.image=$PWM_WORKER_IMAGE" \
      --set worker.imagePullPolicy=IfNotPresent --set worker.cpuCapacity=2 \
      --set worker.memoryCapacityMB=4096 --set worker.maxJobs=1 \
      --wait --timeout 5m >/dev/null
    printf 'Provisioned project %s on %s\n' "$project" "$host"
  done
done < <(jq -r '.[].id' "$scratch/projects.json")
