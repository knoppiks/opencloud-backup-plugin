#!/usr/bin/env bash
# Tests the Kustomize manifests on a real cluster (#159): kind with Calico,
# which enforces NetworkPolicy.
#
#   1. The base's NetworkPolicy. Stand-in pods carry the service's labels and
#      listen where OpenCloud, a backup destination and an unrelated
#      database would. Every connection is first shown to work without the
#      policy, so a refusal afterwards is the policy's and not a broken
#      probe's.
#   2. The web-extension Component, on a stand-in for OpenCloud's Deployment
#      (uid 1000, a fresh data volume) with the web image built from this
#      tree: the extension is where OpenCloud looks for it, and the
#      directories created for it are OpenCloud's to write.
#
# Usage: KIND=… KUSTOMIZE=… PROBE_IMAGE=… CALICO_VERSION=… WEB_IMAGE=… kind-test.sh
# (`make k8s-kind-test` sets them). KEEP_CLUSTER=1 leaves the cluster up.
set -euo pipefail
: "${KIND:?}" "${KUSTOMIZE:?}" "${PROBE_IMAGE:?}" "${CALICO_VERSION:?}" "${WEB_IMAGE:?}"
docker image inspect "$WEB_IMAGE" >/dev/null 2>&1 || {
  echo "no image $WEB_IMAGE: run 'make web-image' first" >&2
  exit 1
}

root=$(git rev-parse --show-toplevel)
cluster=ocbp-netpol
node="${cluster}-control-plane"

cleanup() {
  if [ "${KEEP_CLUSTER:-}" != 1 ]; then
    "$KIND" delete cluster --name "$cluster" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# kubectl runs inside the node: no kubectl or kubeconfig needed on the host.
kubectl() {
  docker exec -i "$node" kubectl --kubeconfig /etc/kubernetes/admin.conf "$@"
}

echo "creating kind cluster ${cluster} without its default CNI..."
"$KIND" delete cluster --name "$cluster" >/dev/null 2>&1 || true
"$KIND" create cluster --name "$cluster" --wait 0s --config - <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16
EOF

echo "installing Calico ${CALICO_VERSION}..."
# create, not apply: Calico's CRDs are too large for apply's annotation.
curl -fsSL "https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml" |
  kubectl create -f - >/dev/null
kubectl -n kube-system rollout status daemonset/calico-node --timeout=5m
kubectl wait --for=condition=Ready node --all --timeout=3m >/dev/null

# --- the stand-ins ----------------------------------------------------------

# pod NAMESPACE NAME LABELS PORTS: a busybox serving "ok" over HTTP on each
# port. LABELS is a YAML flow map.
pod() {
  local ns=$1 name=$2 labels=$3 ports=$4 probe=""
  if [ "$name" = backupd ]; then
    probe='
    readinessProbe:
      httpGet: { path: /, port: 8080 }
      periodSeconds: 2
      failureThreshold: 1'
  fi
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
  namespace: ${ns}
  labels: ${labels}
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: probe
    image: ${PROBE_IMAGE}
    command: ["sh", "-c", "echo ok > /tmp/index.html; for p in ${ports}; do httpd -p \$p -h /tmp; done; exec sleep 100000"]${probe}
EOF
}

for ns in backup ingress opencloud storage other; do
  kubectl create namespace "$ns" >/dev/null
done
backupd_labels='{app.kubernetes.io/name: opencloud-backup, app.kubernetes.io/component: backupd}'
pod backup backupd "$backupd_labels" "8080"
pod ingress controller '{app: controller}' ""
pod opencloud opencloud '{app: opencloud}' "9142 9158 9200"
pod storage s3 '{app: s3}' "443 3900 5432"
pod other db '{app: db}' "5432"
for ns in backup ingress opencloud storage other; do
  kubectl -n "$ns" wait --for=condition=Ready pod --all --timeout=3m >/dev/null
done

ip() { kubectl -n "$1" get pod "$2" -o jsonpath='{.status.podIP}'; }
backupd=$(ip backup backupd)
opencloud=$(ip opencloud opencloud)
s3=$(ip storage s3)
db=$(ip other db)

# reach FROM_NS FROM_POD HOST PORT: one HTTP request, three seconds.
reach() {
  kubectl -n "$1" exec "$2" -- wget -q -T 3 -O /dev/null "http://$3:$4/" >/dev/null 2>&1
}
resolves() {
  kubectl -n backup exec backupd -- nslookup kubernetes.default.svc.cluster.local >/dev/null 2>&1
}

# What the policy allows and what it refuses, as FROM_NS FROM_POD HOST PORT
# DESCRIPTION.
allowed=(
  "ingress controller $backupd 8080 ingress controller to the service"
  "backup backupd $opencloud 9142 the service to OpenCloud's gateway"
  "backup backupd $opencloud 9158 the service to OpenCloud's data server"
  "backup backupd $s3 443 the service to HTTPS anywhere"
  "backup backupd $s3 3900 the service to Garage's S3 port anywhere"
)
refused=(
  "other db $backupd 8080 another namespace to the service"
  "opencloud opencloud $backupd 8080 OpenCloud's namespace to the service"
  "backup backupd $opencloud 9200 the service to OpenCloud's other ports"
  "backup backupd $db 5432 the service to an unrelated database"
  "backup backupd $s3 5432 the service to another port of a destination"
)

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# eventually CMD…: true within 30 s, for a policy still being programmed.
eventually() {
  local _
  for _ in $(seq 15); do
    "$@" && return 0
    sleep 2
  done
  return 1
}

check() {
  local want=$1 from_ns from_pod host port what
  shift
  for entry in "$@"; do
    read -r from_ns from_pod host port what <<<"$entry"
    if [ "$want" = reach ]; then
      if eventually reach "$from_ns" "$from_pod" "$host" "$port"; then
        echo "ok: reached ${what}"
      else
        fail "refused: ${what}"
      fi
    elif reach "$from_ns" "$from_pod" "$host" "$port"; then
      fail "reached: ${what}"
    else
      echo "ok: refused ${what}"
    fi
  done
}

echo "without the policy, everything connects..."
check reach "${allowed[@]}" "${refused[@]}"
resolves || fail "no DNS without the policy"
if [ "$failures" -gt 0 ]; then
  echo "$failures baseline failure(s): the probes are broken, not the policy" >&2
  exit 1
fi

echo "applying the base's NetworkPolicy..."
"$KUSTOMIZE" build "$root/test/kustomize/networkpolicy" |
  awk 'BEGIN { RS = "\n---\n" } /\nkind: NetworkPolicy\n/ { print }' |
  kubectl apply -f - >/dev/null
# Until the first refusal shows, the policy is not in force yet.
eventually sh -c "! docker exec $node kubectl --kubeconfig /etc/kubernetes/admin.conf -n other exec db -- wget -q -T 2 -O /dev/null http://$backupd:8080/ 2>/dev/null" ||
  fail "the policy never took effect"

check reach "${allowed[@]}"
check refuse "${refused[@]}"
eventually resolves || fail "refused: the service to cluster DNS"
sleep 6
[ "$(kubectl -n backup get pod backupd -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = True ] ||
  fail "the kubelet's readiness probe no longer reaches the service"

if [ "$failures" -gt 0 ]; then
  echo "$failures failure(s)" >&2
  exit 1
fi
echo "NetworkPolicy: all cases pass"

# --- the web extension ------------------------------------------------------

echo "loading ${WEB_IMAGE} into the cluster..."
"$KIND" load docker-image "$WEB_IMAGE" --name "$cluster" >/dev/null
kubectl create namespace web >/dev/null
"$KUSTOMIZE" build "$root/test/kustomize/web-extension" |
  sed -e "s#ghcr.io/knoppiks/opencloud-backup-vault-web:unreleased#${WEB_IMAGE}#" \
    -e "s#image: opencloud-stand-in#image: ${PROBE_IMAGE}#" |
  kubectl -n web apply -f - >/dev/null
kubectl -n web rollout status deployment/opencloud --timeout=3m

in_opencloud() { kubectl -n web exec deploy/opencloud -c opencloud -- sh -c "$1" >/dev/null 2>&1; }
apps=/var/lib/opencloud/web/assets/apps
in_opencloud "test -s $apps/backup-vault/manifest.json" ||
  fail "the extension is not at $apps/backup-vault"
# OpenCloud writes under web/assets (themes, logos): the directories the
# initContainer created are its own.
in_opencloud "touch /var/lib/opencloud/web/assets/x $apps/x" ||
  fail "OpenCloud cannot write where the initContainer created directories"

if [ "$failures" -gt 0 ]; then
  echo "$failures failure(s)" >&2
  exit 1
fi
echo "web extension: all cases pass"
