#!/usr/bin/env bash
#
# hashed-configmaps.sh — end-to-end test for AppConfig carried in ConfigMaps
# whose names kustomize hashes (the configMapGenerator default), against a
# live ConfigHub server.
#
# The package, written by this script, has two hashed AppConfig/Env
# generators, a properties generator with disableNameSuffixHash, and a
# Deployment that takes its environment from both env ConfigMaps and mounts
# the properties one.
#
# Flow:
#
#   setup --pull   — renders; out/manifests/ is kustomize's output: hashed
#                    ConfigMap names, and the Deployment's references
#                    rewritten to match. The hashed ConfigMaps are marked
#                    immutable and record their names before the hash.
#   upload         — the AppConfig Units take the names before the hash; each
#                    of the Deployment's references resolves to the ConfigMap
#                    ConfigHub renders from the generator it referred to; the
#                    one to the stable-name ConfigMap is left alone
#   edit + upload  — changing one generator's content and rendering with
#                    --clean, which changes its hashed name, updates its
#                    AppConfig Unit; the new render
#                    is added beside the old one, and with the Space's
#                    prune-configmaps Trigger only the reference to that
#                    generator moves to it
#   re-run         — an unchanged upload writes nothing
#
# The Space is left for inspection. Clean up with:
#
#   cub space delete --recursive <space-slug>
#
# Configuration via env vars:
#   INSTALLER_HASHED_SPACE — override the Space slug.
#                            Default: installer-test-hashed-<YYYYMMDD-HHMMSS>.
#
# Requirements:
#   - go and a working build environment
#   - cub on PATH, authenticated against the server you want to test, which
#     has to be newer than ConfigHub v0.9.10

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
SPACE=${INSTALLER_HASHED_SPACE:-installer-test-hashed-$(date -u +%Y%m%d-%H%M%S)}
NAMESPACE=hashed-e2e
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

[[ "$SPACE" != "default" ]] || fail "INSTALLER_HASHED_SPACE must not be 'default'"

trap 'rc=$?; [[ -n "$WORK_TMP" ]] && printf "\nSpace: %s\nWork-dir: %s\n" "$SPACE" "$WORK_TMP"; exit $rc' EXIT

log "preflight"
command -v cub >/dev/null 2>&1 || fail "cub not on PATH"
cub space list >/dev/null 2>&1 || fail "cub auth not configured (run \`cub auth login\`)"
if cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$SPACE"; then
  fail "Space '$SPACE' already exists. Delete it first or set INSTALLER_HASHED_SPACE."
fi

log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

WORK_TMP=$(mktemp -d -t installer-hashed-e2e.XXXXXX)
PKG="$WORK_TMP/src"
WD="$WORK_TMP/wd"
mkdir -p "$PKG/bases/default"
cat >"$PKG/installer.yaml" <<'EOF'
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: hashed-e2e
installerMetadata:
  version: 0.0.1
spec:
  bases:
    - name: default
      path: bases/default
      default: true
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
      annotations:
        confighub.com/toolchain: AppConfig/Env
  - name: other-env
    envs:
      - other.env
    options:
      annotations:
        confighub.com/toolchain: AppConfig/Env
  - name: app-props
    files:
      - app.properties
    options:
      disableNameSuffixHash: true
      annotations:
        confighub.com/toolchain: AppConfig/Properties
EOF
printf 'GREETING=hello\n' >"$PKG/bases/default/app.env"
printf 'OTHER=other\n' >"$PKG/bases/default/other.env"
printf 'port=5432\n' >"$PKG/bases/default/app.properties"
cat >"$PKG/bases/default/deployment.yaml" <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
      containers:
        - name: app
          image: registry.example.com/app:1.0.0
          envFrom:
            - configMapRef:
                name: app-env
            - configMapRef:
                name: other-env
          volumeMounts:
            - name: props
              mountPath: /etc/app
      volumes:
        - name: props
          configMap:
            name: app-props
EOF

# rendered <generator> prints the name kustomize gave the generator's ConfigMap.
rendered() {
  ls "$WD/out/manifests" | sed -n "s/^configmap-$NAMESPACE-\\($1-[a-z0-9]\\{10\\}\\)\\.yaml\$/\\1/p"
}

# references prints the Deployment Unit's ConfigMap references, comma-separated.
references() {
  cub function do --space "$SPACE" --where "Slug = 'app'" --quiet --show output \
    yq '[.spec.template.spec.containers[0].envFrom[].configMapRef.name, .spec.template.spec.volumes[0].configMap.name] | join(",")'
}

# latest <placeholder-unit> prints the name of the newest ConfigMap ConfigHub rendered into it.
latest() {
  cub function do --space "$SPACE" --where "Slug = '$1'" --quiet --show output \
    yq 'select(.metadata.annotations["confighub.com/RenderRevision"] == "Latest") | .metadata.name'
}

# wait_for_references <expected> polls until the Deployment's references are expected.
wait_for_references() {
  local actual
  for _ in $(seq 1 30); do
    actual=$(references)
    [[ "$actual" == "$1" ]] && return 0
    sleep 1
  done
  fail "the Deployment's references are $actual, want $1"
}

log "setup --pull: kustomize's output, with the hashed ConfigMaps marked immutable"
run setup "$BIN" setup --pull "$PKG" --work-dir "$WD" --non-interactive --namespace "$NAMESPACE" \
  || fail "setup failed (see $WORK_TMP/setup.log)"
app_cm=$(rendered app-env)
other_cm=$(rendered other-env)
[[ -n "$app_cm" && -n "$other_cm" ]] || fail "expected hashed ConfigMaps app-env-<hash> and other-env-<hash> in $WD/out/manifests"
app_file="$WD/out/manifests/configmap-$NAMESPACE-$app_cm.yaml"
grep -q 'confighub.com/appconfig-mutable: "false"' "$app_file" || fail "$app_cm should be marked immutable"
grep -q 'confighub.com/appconfig-name: app-env' "$app_file" || fail "$app_cm should record its name before the hash"
grep -q 'confighub.com/appconfig-mutable: "true"' "$WD/out/manifests/configmap-$NAMESPACE-app-props.yaml" \
  || fail "app-props should be marked mutable"
grep -q "name: $app_cm\$" "$WD/out/manifests/deployment-$NAMESPACE-app.yaml" \
  || fail "the rendered Deployment should refer to $app_cm, as kustomize rewrote it"
note "rendered $app_cm, $other_cm, app-props"

log "upload: Units named before the hash; each reference resolves to its own render"
cub space create --quiet "$SPACE" >/dev/null
cub trigger create --quiet --space "$SPACE" prune-configmaps Mutation Kubernetes/YAML \
  -- prune-configmaps --revision-history-limit=2 >/dev/null
run upload-first "$BIN" upload --work-dir "$WD" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-first.log)"
for slug in app-env app-env-rendered other-env other-env-rendered app-props app-props-rendered app; do
  cub unit get --space "$SPACE" "$slug" --quiet >/dev/null 2>&1 || fail "expected Unit $slug"
done
cub unit get --space "$SPACE" "$app_cm" --quiet >/dev/null 2>&1 && fail "no Unit should take the hashed name $app_cm"
app_render=
other_render=
for _ in $(seq 1 30); do
  app_render=$(latest app-env-rendered)
  other_render=$(latest other-env-rendered)
  [[ -n "$app_render" && -n "$other_render" ]] && break
  sleep 1
done
[[ -n "$app_render" && -n "$other_render" ]] || fail "ConfigHub rendered no ConfigMap from app-env or other-env"
wait_for_references "$app_render,$other_render,app-props"
note "the Deployment refers to $app_render, $other_render, app-props"

log "edit app.env + render + upload: the AppConfig Unit is updated and the reference follows"
printf 'GREETING=bye\n' >"$WD/package/bases/default/app.env"
# --clean, or the ConfigMap under its previous hashed name stays in
# out/manifests/ beside the new one, and upload refuses the AppConfig defined twice.
run render-edit "$BIN" render --work-dir "$WD" --clean || fail "render failed (see $WORK_TMP/render-edit.log)"
[[ "$(rendered app-env)" != "$app_cm" ]] || fail "kustomize should have given app-env a new hashed name"
run upload-edit "$BIN" upload --work-dir "$WD" --space "$SPACE" --yes || fail "upload failed (see $WORK_TMP/upload-edit.log)"
grep -q "^Applied: 0 created, 1 updated, 0 emptied, 0 revived, 0 adopted\\.$" "$WORK_TMP/upload-edit.log" \
  || fail "the edit should update one Unit, app-env, and create none (see $WORK_TMP/upload-edit.log)"
new_render=
for _ in $(seq 1 30); do
  new_render=$(latest app-env-rendered)
  [[ -n "$new_render" && "$new_render" != "$app_render" ]] && break
  sleep 1
done
[[ "$new_render" != "$app_render" ]] || fail "ConfigHub rendered no new ConfigMap from app-env"
wait_for_references "$new_render,$other_render,app-props"
note "the Deployment moved to $new_render; other-env stayed on $other_render"

log "upload (unchanged): writes nothing"
run upload-again "$BIN" upload --work-dir "$WD" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-again.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-again.log" \
  || fail "an unchanged upload should be No changes (see $WORK_TMP/upload-again.log)"

log "PASS"
