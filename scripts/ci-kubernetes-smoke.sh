#!/usr/bin/env bash
set -euo pipefail

# This acceptance test may create and delete Kubernetes resources only in the
# disposable kind chart-testing cluster provisioned by the CI workflow.
if [[ "$(kubectl config current-context)" != "kind-chart-testing" ]]; then
  echo "Refusing to run Kubernetes smoke test outside kind-chart-testing" >&2
  exit 1
fi

for command in kubectl helm openssl curl jq go; do
  command -v "$command" >/dev/null || { echo "Missing $command" >&2; exit 1; }
done

temporary="$(mktemp -d)"
forward_pid=""
cleanup() {
  if [[ -n "$forward_pid" ]]; then
    kill "$forward_pid" 2>/dev/null || true
  fi
  rm -rf "$temporary"
}
trap cleanup EXIT

kubectl create namespace pwm-system
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj "/CN=pwm-controller.pwm-system.svc.cluster.local" \
  -addext "subjectAltName=DNS:pwm-controller.pwm-system.svc.cluster.local,DNS:pwm-controller,DNS:localhost,IP:127.0.0.1" \
  -keyout "$temporary/controller.key" -out "$temporary/controller.crt" >/dev/null 2>&1
openssl rand -hex 32 | tr -d '\n' > "$temporary/admin.token"
chmod 0600 "$temporary/admin.token" "$temporary/controller.key"

kubectl -n pwm-system create secret generic pwm-controller-credentials \
  --from-file=admin_token="$temporary/admin.token"
kubectl -n pwm-system create secret tls pwm-controller-tls \
  --cert="$temporary/controller.crt" --key="$temporary/controller.key"
kubectl -n pwm-system create secret generic pwm-controller-ca \
  --from-file=ca.crt="$temporary/controller.crt"

cat > "$temporary/controller-values.yaml" <<'VALUES'
controller:
  enabled: true
  image: pwm-controller:ci
  imagePullPolicy: IfNotPresent
  allowedHosts: "pwm-controller,pwm-controller.pwm-system.svc.cluster.local,localhost"
  databaseSize: 1Gi
worker:
  enabled: false
VALUES

helm upgrade --install pwm-controller deploy/helm/project-workspace \
  --namespace pwm-system -f "$temporary/controller-values.yaml"
kubectl -n pwm-system rollout status deployment/pwm-controller --timeout=180s

kubectl -n pwm-system port-forward service/pwm-controller 18443:8443 \
  >"$temporary/port-forward.log" 2>&1 &
forward_pid=$!

base="https://localhost:18443"
ready=false
for ((attempt=1; attempt<=60; attempt++)); do
  if curl --silent --show-error --fail --max-time 2 --cacert "$temporary/controller.crt" \
      "$base/health" >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  cat "$temporary/port-forward.log" >&2
  kubectl -n pwm-system describe deployment pwm-controller >&2
  exit 1
fi

admin_token="$(cat "$temporary/admin.token")"
api() {
  curl --silent --show-error --fail --max-time 30 \
    --cacert "$temporary/controller.crt" \
    -H "Authorization: Bearer $admin_token" "$@"
}

api -X POST -H "Content-Type: application/json" \
  -d '{"id":"research","title":"Research"}' \
  "$base/api/projects" > "$temporary/project.json"

pairing_code="$(api -X POST "$base/api/pairing" | jq -r '.pairing_code')"
if [[ ! "$pairing_code" =~ ^[0-9a-f]{48}$ ]]; then
  echo "Controller did not issue a valid pairing code" >&2
  exit 1
fi

go build -trimpath -o "$temporary/project-workspace-worker" ./cmd/project-workspace-worker
printf '%s\n' "$pairing_code" | \
  SSL_CERT_FILE="$temporary/controller.crt" "$temporary/project-workspace-worker" join \
    --backend kubernetes --controller "$base" --node research-agent \
    --project research --namespace pwm-research \
    --capacity-cpu 2 --capacity-memory-mb 2048 --max-jobs 1 \
    --token-file "$temporary/worker.token" --code-stdin

kubectl -n pwm-system create secret generic pwm-worker-research-credentials \
  --from-file=worker_token="$temporary/worker.token"

cat > "$temporary/worker-values.yaml" <<'VALUES'
controller:
  enabled: false
worker:
  enabled: true
  image: pwm-worker:ci
  imagePullPolicy: IfNotPresent
  name: research-agent
  projectID: research
  instanceID: research
  workloadNamespace: pwm-research
  cpuCapacity: 2
  memoryCapacityMB: 2048
  maxJobs: 1
  workspaceStorage: 1Gi
quota:
  cpu: "4"
  memory: 8Gi
  storage: 5Gi
VALUES

helm upgrade --install pwm-research deploy/helm/project-workspace \
  --namespace pwm-system -f "$temporary/worker-values.yaml"
kubectl -n pwm-system rollout status deployment/pwm-worker-research --timeout=150s

online=false
for ((attempt=1; attempt<=90; attempt++)); do
  if api "$base/api/nodes" | jq -e \
      '.[] | select(.id == "research-agent" and .online == true)' >/dev/null; then
    online=true
    break
  fi
  sleep 2
done
if [[ "$online" != true ]]; then
  kubectl -n pwm-system logs deployment/pwm-worker-research --tail=100 >&2 || true
  echo "Kubernetes worker never reported a healthy heartbeat" >&2
  exit 1
fi

workspace_id="$(api -X POST -H 'Content-Type: application/json' \
  -d '{"project_id":"research","image":"debian:bookworm-slim","cpu":0.5,"memory_mb":256,"duration_minutes":5}' \
  "$base/api/workspaces" | jq -r '.id')"
if [[ ! "$workspace_id" =~ ^[0-9a-f]{32}$ ]]; then
  echo "Controller returned an invalid workspace ID" >&2
  exit 1
fi

workspace_url="$base/api/projects/research/workspaces/$workspace_id"
ready=false
for ((attempt=1; attempt<=120; attempt++)); do
  status="$(api "$workspace_url" | jq -r '.status')"
  if [[ "$status" == "ready" ]]; then ready=true; break; fi
  if [[ "$status" == "failed" ]]; then
    kubectl -n pwm-research describe "pod/pwm-$workspace_id" >&2 || true
    echo "Kubernetes workspace admission failed" >&2
    exit 1
  fi
  sleep 2
done
if [[ "$ready" != true ]]; then
  kubectl -n pwm-system logs deployment/pwm-worker-research --tail=120 >&2 || true
  kubectl -n pwm-research get pods,pvc -o wide >&2 || true
  echo "Timed out waiting for a scheduled workspace Pod" >&2
  exit 1
fi

scheduled_node="$(kubectl -n pwm-research get "pod/pwm-$workspace_id" \
  -o jsonpath='{.spec.nodeName}')"
if [[ -z "$scheduled_node" ]]; then
  echo "Kubernetes did not schedule the workspace Pod" >&2
  exit 1
fi

operation_id="$(api -X POST -H 'Content-Type: application/json' \
  -d '{"action":"command","payload":{"command":"printf 42"}}' \
  "$workspace_url/operations" | jq -r '.operation_id')"
if [[ ! "$operation_id" =~ ^[0-9a-f]{32}$ ]]; then
  echo "Controller did not queue the workspace command" >&2
  exit 1
fi
result_ok=false
for ((attempt=1; attempt<=45; attempt++)); do
  result="$(api "$workspace_url/operations/$operation_id")"
  status="$(jq -r '.status' <<<"$result")"
  if [[ "$status" == "succeeded" ]]; then
    [[ "$(jq -r '.result.stdout' <<<"$result")" == "42" ]] || {
      echo "Workspace stdout did not match command result" >&2
      exit 1
    }
    result_ok=true
    break
  fi
  if [[ "$status" == "failed" ]]; then
    echo "Workspace operation failed: $result" >&2
    exit 1
  fi
  sleep 2
done
if [[ "$result_ok" != true ]]; then
  echo "Timed out waiting for Kubernetes command completion" >&2
  exit 1
fi

api -X POST "$workspace_url/stop" > "$temporary/stop.json"
stopped=false
for ((attempt=1; attempt<=45; attempt++)); do
  status="$(api "$workspace_url" | jq -r '.status')"
  if [[ "$status" == "stopped" ]]; then stopped=true; break; fi
  if [[ "$status" == "failed" ]]; then
    echo "Workspace stop failed" >&2
    exit 1
  fi
  sleep 2
done
if [[ "$stopped" != true ]]; then
  echo "Timed out waiting for Kubernetes workspace shutdown" >&2
  exit 1
fi

kubectl -n pwm-research get "pvc/pwm-data-$workspace_id" >/dev/null
echo "Kubernetes Helm installation, worker pairing, Pod execution, and retained PVC verified"
