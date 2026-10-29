#!/usr/bin/env bash
#
# record-reentry.sh — end-to-end test that the installer record ConfigHub holds
# is enough to change an install, with no work-dir kept anywhere else.
#
# Flow:
#
#   setup + upload  — work-dir A installs a package with two inputs and uploads
#                     it. The installer record is uploaded beside the package's
#                     Units, hidden, and says where the package was pulled from.
#   setup --space   — a fresh work-dir B, with no package and no --pull, re-enters
#                     the install from the record: it pulls the package the
#                     record names and starts from its answers. One input is
#                     changed; the other, and the namespace, carry forward.
#   upload          — B's upload changes the one rendered value, and the
#                     record, which stays hidden, now holds the new answer.
#   re-run          — an unchanged upload from B writes nothing.
#
# The Space is left for inspection. Clean up with:
#
#   cub space delete --recursive <space-slug>
#
# Configuration via env vars:
#   INSTALLER_REENTRY_SPACE — override the Space slug.
#                             Default: installer-test-reentry-<YYYYMMDD-HHMMSS>.
#
# Requirements:
#   - go and a working build environment
#   - cub on PATH, authenticated against the server you want to test

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
SPACE=${INSTALLER_REENTRY_SPACE:-installer-test-reentry-$(date -u +%Y%m%d-%H%M%S)}
NAMESPACE=reentry-e2e
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

[[ "$SPACE" != "default" ]] || fail "INSTALLER_REENTRY_SPACE must not be 'default'"

trap 'rc=$?; [[ -n "$WORK_TMP" ]] && printf "\nSpace: %s\nWork-dir: %s\n" "$SPACE" "$WORK_TMP"; exit $rc' EXIT

log "preflight"
command -v cub >/dev/null 2>&1 || fail "cub not on PATH"
cub space list >/dev/null 2>&1 || fail "cub auth not configured (run \`cub auth login\`)"
if cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$SPACE"; then
  fail "Space '$SPACE' already exists. Delete it first or set INSTALLER_REENTRY_SPACE."
fi

log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

WORK_TMP=$(mktemp -d -t installer-reentry-e2e.XXXXXX)
PKG="$WORK_TMP/src"
mkdir -p "$PKG/bases/default"
cat >"$PKG/installer.yaml" <<'EOF'
apiVersion: installer.confighub.com/v1alpha1
kind: Package
metadata:
  name: reentry-e2e
installerMetadata:
  version: 0.0.1
spec:
  bases:
    - name: default
      path: bases/default
      default: true
  inputs:
    - name: image
      type: string
      required: true
      description: The image to run.
    - name: greeting
      type: string
      required: true
      description: What the app says.
  transformers:
    - toolchain: Kubernetes/YAML
      invocations:
        - name: set-namespace
          args: ["{{ .Namespace }}"]
        - name: set-container-image
          args: ["app", "{{ .Inputs.image }}"]
        - name: set-string-path
          args: ["v1/ConfigMap", "data.GREETING", "{{ .Inputs.greeting }}"]
EOF
cat >"$PKG/bases/default/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - deployment.yaml
  - configmap.yaml
EOF
cat >"$PKG/bases/default/configmap.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: app-greeting
data:
  GREETING: confighubplaceholder
EOF
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
          image: confighubplaceholder
          envFrom:
            - configMapRef:
                name: app-greeting
EOF

# unit_value <unit-slug> <yq-expression> prints a value from a Unit's data.
unit_value() {
  cub function do --space "$SPACE" --where "Slug = '$1'" --quiet --show output yq "$2"
}

# record_value <yq-expression> prints a value from the installer record, which
# a where naming its Slug selects although it is hidden.
record_value() {
  cub unit data --space "$SPACE" installer | yq -r "$1"
}

log "setup + upload in work-dir A"
A="$WORK_TMP/a"
run setup-a "$BIN" setup --pull "$PKG" --work-dir "$A" --non-interactive --namespace "$NAMESPACE" \
  --input image=registry.example.com/app:1.0.0 --input greeting=hello \
  || fail "setup failed (see $WORK_TMP/setup-a.log)"
cub space create --quiet "$SPACE" >/dev/null
run upload-a "$BIN" upload --work-dir "$A" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-a.log)"
[[ "$(unit_value app-greeting-configmap '.data.GREETING')" == hello ]] || fail "the ConfigMap should say hello"

[[ "$(cub unit get --space "$SPACE" installer --quiet -o jq='.Unit.HiddenReason')" == InstallerRecord ]] \
  || fail "the installer record should be hidden with the reason InstallerRecord"
cub unit list --space "$SPACE" --no-headers -o name 2>/dev/null | grep -qx "$SPACE/installer" \
  && fail "a hidden installer record should not be listed"
cub unit list --space "$SPACE" --no-headers -o name --include-hidden=InstallerRecord 2>/dev/null | grep -qx "$SPACE/installer" \
  || fail "--include-hidden=InstallerRecord should list the installer record"
[[ "$(record_value '.source.ref')" == "$PKG" ]] || fail "the record should say the package came from $PKG"
note "the record is hidden and says the package came from $PKG"

log "setup --space in a fresh work-dir B, changing one input, with no --pull"
B="$WORK_TMP/b"
run setup-b "$BIN" setup --space "$SPACE" --work-dir "$B" --non-interactive --input greeting=bye \
  || fail "setup failed (see $WORK_TMP/setup-b.log)"
grep -q "Pulling $PKG, which the installer record in Space $SPACE says" "$WORK_TMP/setup-b.log" \
  || fail "setup should pull the package the record names (see $WORK_TMP/setup-b.log)"
grep -q "Loaded prior install state from confighub" "$WORK_TMP/setup-b.log" \
  || fail "setup should start from the record's answers (see $WORK_TMP/setup-b.log)"

log "upload from B: only the changed input's value changes"
run upload-b "$BIN" upload --work-dir "$B" --space "$SPACE" --yes || fail "upload failed (see $WORK_TMP/upload-b.log)"
[[ "$(unit_value app-greeting-configmap '.data.GREETING')" == bye ]] || fail "the ConfigMap should say bye"
[[ "$(unit_value app '.spec.template.spec.containers[0].image')" == registry.example.com/app:1.0.0 ]] \
  || fail "the image should carry forward from the record"
[[ "$(unit_value app '.metadata.namespace')" == "$NAMESPACE" ]] || fail "the namespace should carry forward from the record"
[[ "$(record_value '.inputs.values.greeting')" == bye ]] || fail "the record should hold the new answer"
[[ "$(cub unit get --space "$SPACE" installer --quiet -o jq='.Unit.HiddenReason')" == InstallerRecord ]] \
  || fail "the installer record should stay hidden"

log "upload from B again: writes nothing"
run upload-again "$BIN" upload --work-dir "$B" --space "$SPACE" || fail "upload failed (see $WORK_TMP/upload-again.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-again.log" \
  || fail "an unchanged upload should be No changes (see $WORK_TMP/upload-again.log)"

log "PASS"
