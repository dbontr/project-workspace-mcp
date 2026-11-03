#!/usr/bin/env bash
set -euo pipefail

# Run only against the disposable kind cluster provisioned by CI.
if [[ "$(kubectl config current-context)" != "kind-chart-testing" ]]; then
  echo "Refusing recovery test outside kind-chart-testing" >&2
  exit 1
fi
for executable in kubectl helm openssl jq grep awk; do
  command -v "$executable" >/dev/null || {
    echo "Missing $executable" >&2
    exit 1
  }
done
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
namespace="pwm-system"
repository="rest:http://pwm-restic-store.pwm-system.svc.cluster.local:8000/controller"

# The independent HTTP backend is unprotected only inside disposable kind.
# Production repositories require authenticated transport and remote storage.
cat > "$scratch/restic-server.yaml" <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pwm-restic-ci-repository
  namespace: pwm-system
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 2Gi
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pwm-restic-store
  namespace: pwm-system
spec:
  replicas: 1
  selector:
    matchLabels:
      app: pwm-restic-store
  template:
    metadata:
      labels:
        app: pwm-restic-store
    spec:
      automountServiceAccountToken: false
      containers:
        - name: restic-rest-server
          image: restic/rest-server:0.14.0
          env:
            - name: DISABLE_AUTHENTICATION
              value: "1"
          ports:
            - containerPort: 8000
          volumeMounts:
            - name: repository
              mountPath: /data
          readinessProbe:
            tcpSocket:
              port: 8000
            periodSeconds: 2
      volumes:
        - name: repository
          persistentVolumeClaim:
            claimName: pwm-restic-ci-repository
---
apiVersion: v1
kind: Service
metadata:
  name: pwm-restic-store
  namespace: pwm-system
spec:
  selector:
    app: pwm-restic-store
  ports:
    - name: http
      port: 8000
      targetPort: 8000
YAML
kubectl apply -f "$scratch/restic-server.yaml"
kubectl -n "$namespace" rollout status deployment/pwm-restic-store --timeout=180s

openssl rand -hex 40 > "$scratch/restic-password"
chmod 0600 "$scratch/restic-password"
kubectl -n "$namespace" create secret generic pwm-restic-ci-password \
  --from-file=password="$scratch/restic-password"

cat > "$scratch/backup-values.yaml" <<'YAML'
controller:
  allowedHosts: "pwm-controller,pwm-controller.pwm-system.svc.cluster.local,localhost"
  backupInterval: 24h
  offsite:
    repository: rest:http://pwm-restic-store.pwm-system.svc.cluster.local:8000/controller
    passwordSecretName: pwm-restic-ci-password
YAML
helm upgrade pwm-controller deploy/helm/project-workspace \
  -n "$namespace" -f "$scratch/backup-values.yaml" \
  --set worker.enabled=false \
  --set controller.image=pwm-controller:ci \
  --set controller.imagePullPolicy=IfNotPresent \
  --set controller.databaseSize=1Gi
kubectl -n "$namespace" rollout status deployment/pwm-controller --timeout=180s

# Initialize the network repository using the actual Helm-mounted 0600 secret.
kubectl -n "$namespace" exec deployment/pwm-controller -- \
  restic -r "$repository" --password-file /run/restic/password init

# Stop the authoritative SQLite writer before invoking offline CLI commands
# from Kubernetes Jobs that mount the source PVC.
kubectl -n "$namespace" scale deployment/pwm-controller --replicas=0
remaining="-1"
for ((attempt=1; attempt<=60; attempt++)); do
  remaining="$(kubectl -n "$namespace" get pods -l app=pwm-controller -o json | jq '.items | length')"
  if [[ "$remaining" == "0" ]]; then
    break
  fi
  sleep 2
done
if [[ "$remaining" != "0" ]]; then
  echo "Controller still holds the SQLite volume" >&2
  exit 1
fi

# Convert the installed Helm controller Pod spec into a one-shot Job,
# preserving credentials and securityContext but removing server probes.
make_job() {
  local name="$1" action="$2" snapshot="$3" claim="$4"
  kubectl -n "$namespace" get deployment pwm-controller -o json | jq \
    --arg name "$name" --arg action "$action" --arg snapshot "$snapshot" --arg claim "$claim" '
    {
      apiVersion: "batch/v1",
      kind: "Job",
      metadata: {name: $name, namespace: "pwm-system"},
      spec: {
        backoffLimit: 0,
        template: {
          metadata: {labels: {app: $name}},
          spec: (
            .spec.template.spec
            | .restartPolicy = "Never"
            | .containers |= map(
                del(.readinessProbe, .livenessProbe)
                | .args = (if $snapshot == "" then [$action] else [$action, $snapshot] end)
              )
            | .volumes |= map(
                if .name == "state" then
                  .persistentVolumeClaim.claimName = $claim
                else . end
              )
          )
        }
      }
    }' | kubectl apply -f -
}

make_job pwm-restic-ci-backup backup-offsite "" pwm-controller-state
if ! kubectl -n "$namespace" wait --for=condition=complete job/pwm-restic-ci-backup --timeout=240s; then
  kubectl -n "$namespace" logs job/pwm-restic-ci-backup --all-containers=true >&2 || true
  exit 1
fi
backup_output="$(kubectl -n "$namespace" logs job/pwm-restic-ci-backup -c controller)"
snapshot="$(grep -Eo 'Restic snapshot [0-9a-f]{8,64}' <<<"$backup_output" | awk '{print $3}')"
if [[ ! "$snapshot" =~ ^[0-9a-f]{8,64}$ ]]; then
  echo "Restic backup did not return an exact snapshot ID" >&2
  exit 1
fi

make_job pwm-restic-ci-verify verify-offsite "$snapshot" pwm-controller-state
if ! kubectl -n "$namespace" wait --for=condition=complete job/pwm-restic-ci-verify --timeout=240s; then
  kubectl -n "$namespace" logs job/pwm-restic-ci-verify --all-containers=true >&2 || true
  exit 1
fi

kubectl -n "$namespace" apply -f - <<'YAML'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pwm-restic-ci-recovered
  namespace: pwm-system
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Gi
YAML
make_job pwm-restic-ci-restore restore-offsite "$snapshot" pwm-restic-ci-recovered
if ! kubectl -n "$namespace" wait --for=condition=complete job/pwm-restic-ci-restore --timeout=240s; then
  kubectl -n "$namespace" logs job/pwm-restic-ci-restore --all-containers=true >&2 || true
  exit 1
fi

# A clean controller invocation must open the restored database successfully.
make_job pwm-restic-ci-open pair "" pwm-restic-ci-recovered
if ! kubectl -n "$namespace" wait --for=condition=complete job/pwm-restic-ci-open --timeout=180s; then
  kubectl -n "$namespace" logs job/pwm-restic-ci-open --all-containers=true >&2 || true
  exit 1
fi
kubectl -n "$namespace" logs job/pwm-restic-ci-open -c controller | \
  grep -Eq '^Pairing code \(10 min\): [0-9a-f]{48}$'

kubectl -n "$namespace" scale deployment/pwm-controller --replicas=1
kubectl -n "$namespace" rollout status deployment/pwm-controller --timeout=180s
echo "Network Restic snapshot, integrity check, clean-PVC restore and controller readback verified"
