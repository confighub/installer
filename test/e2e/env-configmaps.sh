#!/usr/bin/env bash
#
# env-configmaps.sh — end-to-end test for an environment carried in a
# ConfigMap that kustomize generates from env files (a configMapGenerator with
# envs:), against a live ConfigHub server, and optionally a cluster.
#
# The package, written by this script, generates one ConfigMap, app-env, from
# two env files: the base's app.env and the extra component's extra.env, which
# merges into it. Its name is stable (disableNameSuffixHash). A Deployment takes
# its whole environment from it with envFrom, and its pod template asks for
# ConfigHub's confighub.com/Hash. The values cover what an env file can hold:
# spaces, '=' and URL punctuation, an empty value, and a number, which a
# ConfigMap has to hold as a string.
#
# Flow:
#
#   setup --pull   — kustomize merges both env files into app-env, under its
#                    own name, which the installer marks mutable
#   upload         — app-env becomes an AppConfig/Env Unit holding the env
#                    file; ConfigHub renders it into app-env-rendered as a
#                    key-value ConfigMap: one data key per variable, the
#                    configHub metadata left out, under the name the
#                    Deployment's envFrom names, with the Deployment's
#                    confighub.com/Hash filled in with the ConfigMap's
#   edit in ConfigHub
#                  — set-string-path on the AppConfig Unit changes one
#                    variable and adds another; ConfigHub renders the
#                    ConfigMap again in place, and the Deployment's hash
#                    follows it, so its pods roll
#   cluster        — with INSTALLER_ENV_KUBE_CONTEXT, the rendered ConfigMap
#                    and Deployment are applied, and the pod's environment is
#                    exactly what ConfigHub rendered
#   re-run         — an unchanged upload writes nothing, and keeps the edits
#                    made in ConfigHub
#
# The Space, and the namespace in the cluster, are left for inspection. Clean up
# with:
#
#   cub space delete --recursive <space-slug>
#   kubectl --context <context> delete namespace env-e2e
#
# Configuration via env vars:
#   INSTALLER_ENV_SPACE        — override the Space slug.
#                                Default: installer-test-env-<YYYYMMDD-HHMMSS>.
#   INSTALLER_ENV_KUBE_CONTEXT — a kubectl context to deploy to, such as a kind
#                                cluster's. Unset skips the cluster checks.
#                                The cluster pulls busybox from Docker Hub.
#
# Requirements:
#   - go and a working build environment, yq and jq
#   - cub on PATH, authenticated against the server you want to test
#   - with INSTALLER_ENV_KUBE_CONTEXT, kubectl

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
SPACE=${INSTALLER_ENV_SPACE:-installer-test-env-$(date -u +%Y%m%d-%H%M%S)}
KUBE_CONTEXT=${INSTALLER_ENV_KUBE_CONTEXT:-}
NAMESPACE=env-e2e
WORK_TMP=

log()  { printf '\n=== %s ===\n' "$*" >&2; }
note() { printf '    %s\n' "$*" >&2; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# run <log-name> <cmd> [args...] runs the command with its output in
# $WORK_TMP/<log-name>.log.
run() {
  local logname=$1
  shift
  note "→ $logname.log: $*"
  "$@" >"$WORK_TMP/$logname.log" 2>&1
}

[[ "$SPACE" != "default" ]] || fail "INSTALLER_ENV_SPACE must not be 'default'"

trap 'rc=$?; [[ -n "$WORK_TMP" ]] && printf "\nSpace: %s\nWork-dir: %s\n" "$SPACE" "$WORK_TMP"; exit $rc' EXIT

log "preflight"
for tool in cub yq jq; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool not on PATH"
done
if [[ -n "$KUBE_CONTEXT" ]]; then
  command -v kubectl >/dev/null 2>&1 || fail "kubectl not on PATH"
  kubectl --context "$KUBE_CONTEXT" get namespace default >/dev/null || fail "can't reach the cluster of context $KUBE_CONTEXT"
fi
cub space list >/dev/null 2>&1 || fail "cub auth not configured (run \`cub auth login\`)"
if cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$SPACE"; then
  fail "Space '$SPACE' already exists. Delete it first or set INSTALLER_ENV_SPACE."
fi

log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

WORK_TMP=$(mktemp -d -t installer-env-e2e.XXXXXX)
PKG="$WORK_TMP/src"
WD="$WORK_TMP/wd"
mkdir -p "$PKG/bases/default" "$PKG/components/extra"
cat >"$PKG/installer.yaml" <<'EOF'
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: env-e2e
installerMetadata:
  version: 0.0.1
spec:
  bases:
    - name: default
      path: bases/default
      default: true
  components:
    - name: extra
      path: components/extra
  transformers:
    - toolchain: Kubernetes/YAML
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
EOF
cat >"$PKG/bases/default/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
configMapGenerator:
  - name: app-env
    envs:
      - app.env
    options:
      disableNameSuffixHash: true
      annotations:
        confighub.com/toolchain: AppConfig/Env
EOF
cat >"$PKG/bases/default/app.env" <<'EOF'
configHub.configName=app-env
configHub.configSchema=App
GREETING=hello world
DATABASE_URL=postgres://app:s3cret@db.example.com:5432/app?sslmode=require&x=1
EMPTY=
PORT=8080
EOF
cat >"$PKG/components/extra/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1alpha1
kind: Component
configMapGenerator:
  - name: app-env
    behavior: merge
    envs:
      - extra.env
EOF
printf 'EXTRA_FLAG=on\n' >"$PKG/components/extra/extra.env"
cat >"$PKG/bases/default/deployment.yaml" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  replicas: 1
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
      annotations:
        confighub.com/Hash: confighubplaceholder
    spec:
      terminationGracePeriodSeconds: 1
      containers:
        - name: app
          image: busybox:1.36.1
          # Prints the environment it was given, for the cluster check.
          command: ["sh", "-c", "env; sleep 3600"]
          envFrom:
            - configMapRef:
                name: app-env
EOF

# The variables the env files set, as the rendered ConfigMap's data has to hold
# them.
expected_data='{
  "GREETING": "hello world",
  "DATABASE_URL": "postgres://app:s3cret@db.example.com:5432/app?sslmode=require&x=1",
  "EMPTY": "",
  "PORT": "8080",
  "EXTRA_FLAG": "on"
}'

# unit_yaml <unit> <yq expression> evaluates the expression on the Unit's data.
unit_yaml() {
  cub unit data --space "$SPACE" "$1" 2>/dev/null | yq -r "$2"
}

# rendered_data prints the rendered ConfigMap's data as sorted JSON.
rendered_data() {
  cub unit data --space "$SPACE" app-env-rendered 2>/dev/null | yq -o json '.data // {}' | jq -S .
}

# wait_for_data <expected JSON> polls until the rendered ConfigMap's data is
# expected.
wait_for_data() {
  local want actual
  want=$(jq -S . <<<"$1")
  for _ in $(seq 1 30); do
    actual=$(rendered_data)
    [[ "$actual" == "$want" ]] && return 0
    sleep 1
  done
  printf 'want:\n%s\ngot:\n%s\n' "$want" "$actual" >&2
  fail "the rendered ConfigMap's data is not the env file's variables"
}

# wait_for_pod_hash <hash> polls until the Deployment's pod template carries
# the hash.
wait_for_pod_hash() {
  local actual
  for _ in $(seq 1 30); do
    actual=$(unit_yaml app '.spec.template.metadata.annotations["confighub.com/Hash"]')
    [[ "$actual" == "$1" ]] && return 0
    sleep 1
  done
  fail "the Deployment's pod template has confighub.com/Hash $actual, want the rendered ConfigMap's, $1"
}

# check_rendered_configmap checks what the rendered ConfigMap is besides its
# data: the only resource in its Unit, under the name the Deployment's envFrom
# names, in the install namespace.
check_rendered_configmap() {
  [[ "$(cub unit data --space "$SPACE" app-env-rendered | yq ea '[.] | length')" == 1 ]] \
    || fail "app-env-rendered should hold one ConfigMap, rendered in place"
  [[ "$(unit_yaml app-env-rendered .kind)" == ConfigMap ]] || fail "app-env-rendered should hold a ConfigMap"
  [[ "$(unit_yaml app-env-rendered .metadata.name)" == app-env ]] \
    || fail "the rendered ConfigMap is named $(unit_yaml app-env-rendered .metadata.name), want app-env, its generator's name"
  [[ "$(unit_yaml app-env-rendered .metadata.namespace)" == "$NAMESPACE" ]] \
    || fail "the rendered ConfigMap is in namespace $(unit_yaml app-env-rendered .metadata.namespace), want $NAMESPACE"
  [[ "$(unit_yaml app '.spec.template.spec.containers[0].envFrom[0].configMapRef.name')" == app-env ]] \
    || fail "the Deployment's envFrom names $(unit_yaml app '.spec.template.spec.containers[0].envFrom[0].configMapRef.name'), want app-env"
}

log "setup --pull: both env files in one ConfigMap under its generator's name, marked mutable"
run setup "$BIN" setup --pull "$PKG" --work-dir "$WD" --non-interactive --namespace "$NAMESPACE" \
  --select extra || fail "setup failed (see $WORK_TMP/setup.log)"
cm_file="$WD/out/manifests/configmap-$NAMESPACE-app-env.yaml"
[[ -f "$cm_file" ]] || fail "expected $cm_file: kustomize should keep app-env's name"
[[ "$(yq -r '.metadata.annotations["confighub.com/appconfig-mutable"]' "$cm_file")" == true ]] \
  || fail "app-env should be marked mutable"
[[ "$(yq -r '.metadata.annotations["confighub.com/appconfig-mode"]' "$cm_file")" == env ]] \
  || fail "app-env should be marked as made from env files"
[[ "$(yq -r .data.EXTRA_FLAG "$cm_file")" == on ]] || fail "app-env should hold the component's EXTRA_FLAG"
[[ "$(yq -r .data.GREETING "$cm_file")" == "hello world" ]] || fail "app-env should hold the base's GREETING"
note "rendered app-env from app.env and extra.env"

log "upload: an AppConfig/Env Unit, rendered by ConfigHub into a key-value ConfigMap"
run upload-first "$BIN" upload --work-dir "$WD" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-first.log)"
for slug in app-env app-env-rendered app; do
  cub unit get --space "$SPACE" "$slug" --quiet >/dev/null 2>&1 || fail "expected Unit $slug"
done
[[ "$(cub unit get --space "$SPACE" app-env --quiet -o jq=.Unit.ToolchainType)" == AppConfig/Env ]] \
  || fail "app-env should be an AppConfig/Env Unit"
cub unit data --space "$SPACE" app-env >"$WORK_TMP/app-env.env"
for line in 'GREETING=hello world' 'PORT=8080' 'EMPTY=' 'EXTRA_FLAG=on'; do
  grep -qxF "$line" "$WORK_TMP/app-env.env" || fail "AppConfig Unit app-env should hold the line $line (see $WORK_TMP/app-env.env)"
done
wait_for_data "$expected_data"
check_rendered_configmap
first_hash=$(unit_yaml app-env-rendered '.metadata.annotations["confighub.com/Hash"]')
[[ -n "$first_hash" && "$first_hash" != null ]] || fail "the rendered ConfigMap has no confighub.com/Hash"
wait_for_pod_hash "$first_hash"
note "app-env-rendered holds app-env with each variable as a data key; the Deployment has its hash $first_hash"

log "edit in ConfigHub: set-string-path on the AppConfig Unit renders the ConfigMap again in place"
run edit cub function set --space "$SPACE" --unit app-env --toolchain AppConfig/Env --quiet \
  set-string-path App GREETING "goodbye world" || fail "set-string-path failed (see $WORK_TMP/edit.log)"
run add cub function set --space "$SPACE" --unit app-env --toolchain AppConfig/Env --quiet \
  set-string-path App ADDED "added in ConfigHub" || fail "set-string-path failed (see $WORK_TMP/add.log)"
edited_data=$(jq '.GREETING = "goodbye world" | .ADDED = "added in ConfigHub"' <<<"$expected_data")
wait_for_data "$edited_data"
check_rendered_configmap
second_hash=$(unit_yaml app-env-rendered '.metadata.annotations["confighub.com/Hash"]')
[[ "$second_hash" != "$first_hash" ]] || fail "the rendered ConfigMap's hash should change with its data"
wait_for_pod_hash "$second_hash"
note "app-env-rendered updated in place; the Deployment moved to hash $second_hash"

if [[ -n "$KUBE_CONTEXT" ]]; then
  log "cluster: the pod's environment is the rendered ConfigMap's data"
  kubectl() { command kubectl --context "$KUBE_CONTEXT" "$@"; }
  kubectl create namespace "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  cub unit data --space "$SPACE" app-env-rendered >"$WORK_TMP/configmap.yaml"
  cub unit data --space "$SPACE" app >"$WORK_TMP/deployment.yaml"
  run apply kubectl apply -f "$WORK_TMP/configmap.yaml" -f "$WORK_TMP/deployment.yaml" \
    || fail "apply failed (see $WORK_TMP/apply.log)"
  run rollout kubectl -n "$NAMESPACE" rollout status deployment/app --timeout=180s \
    || fail "the Deployment didn't roll out (see $WORK_TMP/rollout.log)"
  pod=$(kubectl -n "$NAMESPACE" get pods -l app=app --field-selector=status.phase=Running \
    -o jsonpath='{.items[0].metadata.name}')
  [[ "$(kubectl -n "$NAMESPACE" get pod "$pod" -o jsonpath='{.metadata.annotations.confighub\.com/Hash}')" == "$second_hash" ]] \
    || fail "pod $pod doesn't carry the rendered ConfigMap's hash $second_hash"
  kubectl -n "$NAMESPACE" logs "$pod" >"$WORK_TMP/pod-env.txt"
  for key in $(jq -r 'keys[]' <<<"$edited_data"); do
    line="$key=$(jq -r --arg k "$key" '.[$k]' <<<"$edited_data")"
    grep -qxF "$line" "$WORK_TMP/pod-env.txt" || fail "pod $pod's environment lacks $line (see $WORK_TMP/pod-env.txt)"
  done
  grep -q '^configHub\.' "$WORK_TMP/pod-env.txt" && fail "pod $pod's environment holds configHub metadata (see $WORK_TMP/pod-env.txt)"
  note "pod $pod runs with every variable ConfigHub rendered"
else
  note "INSTALLER_ENV_KUBE_CONTEXT unset: skipping the cluster checks"
fi

log "upload (unchanged): writes nothing"
run upload-again "$BIN" upload --work-dir "$WD" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-again.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-again.log" \
  || fail "an unchanged upload should be No changes (see $WORK_TMP/upload-again.log)"
wait_for_data "$edited_data"
note "the edits made in ConfigHub stay"

log "PASS"
