#!/usr/bin/env bash
#
# package-and-deps.sh — end-to-end smoke for the package + dependency
# pipeline. Drives the installer binary through:
#
#   build → start registry → push example-base → setup (multi-package,
#   includes pull + wizard + deps update + render) → assertions →
#   (optional) upload to ConfigHub + reconcile + upgrade flow.
#
# Requirements:
#   - go and a working build environment
#   - docker (registry:2 is pulled on demand)
#   - kustomize on PATH (render shells out to it)
#
# Optional:
#   - cub on PATH and authenticated (INSTALLER_E2E_CONFIGHUB=1 runs
#     upload + reconcile against the live server). Upload uses Spaces
#     prefixed installer-e2e-* and tears them down on exit.
#
# Exit codes:
#   0  pipeline succeeded
#   1  pipeline failed; failure point printed on stderr
#
# All temp state lives in $(mktemp -d) and is removed on exit unless
# INSTALLER_E2E_KEEP=1.

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK_TMP=
REGISTRY_NAME=installer-e2e-registry
REGISTRY_PORT=5555
DO_UPLOAD=${INSTALLER_E2E_CONFIGHUB:-0}
KEEP=${INSTALLER_E2E_KEEP:-0}

# Spaces created by the upload step. Names track the --space-pattern
# below; cleaned up on exit.
UPLOAD_SPACES=(installer-e2e-example-stack installer-e2e-example-base)

log() { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  set +e
  if [[ "$DO_UPLOAD" = "1" ]]; then
    for s in "${UPLOAD_SPACES[@]}"; do
      cub space delete --recursive --quiet "$s" >/dev/null 2>&1
    done
  fi
  docker rm -f "$REGISTRY_NAME" >/dev/null 2>&1
  if [[ "$KEEP" != "1" && -n "$WORK_TMP" ]]; then
    rm -rf "$WORK_TMP"
  else
    echo "preserved: $WORK_TMP"
  fi
}
trap cleanup EXIT

# 1. Build.
log "build installer"
( cd "$REPO_ROOT" && go build -o bin/install ./cmd/installer )
BIN="$REPO_ROOT/bin/install"

# 2. Registry.
log "start registry:2 on :$REGISTRY_PORT"
docker rm -f "$REGISTRY_NAME" >/dev/null 2>&1
docker run -d --name "$REGISTRY_NAME" -p "$REGISTRY_PORT:5000" registry:2 >/dev/null
for i in {1..30}; do
  if curl -s -o /dev/null -w "%{http_code}" "http://localhost:$REGISTRY_PORT/v2/" | grep -q 200; then break; fi
  sleep 0.2
done

# 3. Push example-base.
log "push example-base"
"$BIN" push "$REPO_ROOT/examples/example-base" \
  "oci://localhost:$REGISTRY_PORT/installer-e2e/example-base:0.1.0" >/dev/null

# 4. Setup: pull + wizard + deps update + render in one call.
WORK_TMP=$(mktemp -d -t installer-e2e.XXXXXX)
log "setup --pull (multi-package)"
"$BIN" setup \
  --pull "$REPO_ROOT/examples/example-stack" \
  --work-dir "$WORK_TMP" \
  --non-interactive \
  --namespace e2e-ns >/dev/null

# 5. Assertions.
log "assertions"
parent_manifests="$WORK_TMP/out/manifests"
dep_manifests="$WORK_TMP/out/base/manifests"
lock="$WORK_TMP/out/spec/lock.yaml"

[[ -d "$parent_manifests" ]] || fail "parent manifests dir missing: $parent_manifests"
[[ -d "$dep_manifests"    ]] || fail "dep manifests dir missing: $dep_manifests"
[[ -f "$lock"             ]] || fail "lock file missing: $lock"
[[ -d "$WORK_TMP/package" ]] || fail "setup --pull should produce $WORK_TMP/package/"
[[ ! -d "$WORK_TMP/.upgrade" ]] || fail "setup should NOT create .upgrade/ (atomic pull replaces package/)"

# Parent should render exactly the Deployment.
parent_count=$(find "$parent_manifests" -type f -name '*.yaml' | wc -l | tr -d ' ')
[[ "$parent_count" = "1" ]] || fail "parent has $parent_count manifests, want 1"
grep -q 'kind: Deployment' "$parent_manifests"/*.yaml || fail "parent manifest is not a Deployment"
grep -q '^  namespace: e2e-ns' "$parent_manifests"/*.yaml || fail "parent Deployment missing namespace: e2e-ns"

# Dep should render exactly Namespace + ConfigMap, both in e2e-ns.
dep_count=$(find "$dep_manifests" -type f -name '*.yaml' | wc -l | tr -d ' ')
[[ "$dep_count" = "2" ]] || fail "dep has $dep_count manifests, want 2"
ns_file="$dep_manifests/namespace-e2e-ns.yaml"
cm_file="$dep_manifests/configmap-e2e-ns-example-base-defaults.yaml"
[[ -f "$ns_file" ]] || fail "missing $ns_file"
[[ -f "$cm_file" ]] || fail "missing $cm_file"
grep -q '^  name: e2e-ns$' "$ns_file" || fail "Namespace not renamed to e2e-ns"

# Lock should pin the dep digest.
grep -q 'name: base' "$lock" || fail "lock missing dep entry"
grep -q '^      digest: sha256:' "$lock" || fail "lock missing pinned digest"

echo "render assertions passed: parent=1 manifest, dep=2 manifests, lock has pinned digest"

# 6. Determinism: re-run setup --pull into a fresh dir and confirm
#    every output file's sha256 matches.
log "determinism"
WORK_TMP2=$(mktemp -d -t installer-e2e-2.XXXXXX)
"$BIN" setup --pull "$REPO_ROOT/examples/example-stack" \
  --work-dir "$WORK_TMP2" --non-interactive --namespace e2e-ns >/dev/null
diff -q -r \
  <(find "$WORK_TMP/out" -type f -not -path '*/vendor/*' -exec shasum -a 256 {} \; | awk '{print $1, $2}' | sed "s|$WORK_TMP/||" | sort) \
  <(find "$WORK_TMP2/out" -type f -not -path '*/vendor/*' -exec shasum -a 256 {} \; | awk '{print $1, $2}' | sed "s|$WORK_TMP2/||" | sort) \
  >/dev/null || fail "rendered output differs across two runs"
echo "determinism: out/ trees byte-identical across two renders"
rm -rf "$WORK_TMP2"

# 7. Re-run setup WITHOUT --pull on the same work-dir → should re-load
#    the existing package, schema-diff (no diff since same package),
#    and re-render byte-identical output.
log "setup re-run without --pull (re-render from existing package)"
mkdir -p "$WORK_TMP/out/manifests.bak" && cp "$WORK_TMP/out/manifests"/*.yaml "$WORK_TMP/out/manifests.bak/"
"$BIN" setup --work-dir "$WORK_TMP" --non-interactive >/dev/null
diff -q -r "$WORK_TMP/out/manifests" "$WORK_TMP/out/manifests.bak" >/dev/null \
  || fail "second setup without --pull should produce byte-identical output"
rm -rf "$WORK_TMP/out/manifests.bak"
echo "setup (no --pull): re-render byte-identical against prior state"

# 8. Optional upload + reconcile + upgrade against ConfigHub.
if [[ "$DO_UPLOAD" = "1" ]]; then
  log "upload to ConfigHub (--space-pattern installer-e2e-{{.PackageName}})"
  if ! command -v cub >/dev/null 2>&1; then
    fail "INSTALLER_E2E_CONFIGHUB=1 but cub not on PATH"
  fi
  if ! cub space list >/dev/null 2>&1; then
    fail "cub auth not configured (run \`cub auth login\`)"
  fi
  # Prefix all Spaces so cleanup picks them up.
  "$BIN" upload --work-dir "$WORK_TMP" --space-pattern 'installer-e2e-{{.PackageName}}'

  for s in "${UPLOAD_SPACES[@]}"; do
    if ! cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$s"; then
      fail "expected Space $s not found after upload"
    fi
  done
  echo "Spaces created:"
  for s in "${UPLOAD_SPACES[@]}"; do
    echo "  $s"
    cub unit list --space "$s" 2>&1 | awk 'NR>1 {print "    "$1}'
  done

  log "plan against unchanged work-dir"
  "$BIN" plan --work-dir "$WORK_TMP" 2>&1 | tee "$WORK_TMP/plan-clean.out"
  if ! grep -q "^No changes\\.$" "$WORK_TMP/plan-clean.out"; then
    fail "plan against just-uploaded work-dir should report No changes"
  fi

  log "plan after editing one rendered manifest"
  # Pick the first .yaml file under example-base manifests and inject a
  # marker label so the plan must surface a change.
  PARENT_DIR="$WORK_TMP/out/example-base/manifests"
  if [[ ! -d "$PARENT_DIR" ]]; then
    PARENT_DIR="$WORK_TMP/out/manifests"
  fi
  EDIT_FILE=$(ls "$PARENT_DIR"/*.yaml 2>/dev/null | head -1)
  [[ -n "$EDIT_FILE" ]] || fail "no rendered manifest to edit under $PARENT_DIR"
  python3 -c "
import sys, yaml
p = sys.argv[1]
with open(p) as f:
    docs = list(yaml.safe_load_all(f))
for d in docs:
    if isinstance(d, dict) and isinstance(d.get('metadata'), dict):
        labels = d['metadata'].setdefault('labels', {})
        labels['installer-e2e-marker'] = 'true'
        break
with open(p, 'w') as f:
    yaml.safe_dump_all(docs, f, default_flow_style=False, sort_keys=False)
" "$EDIT_FILE"

  "$BIN" plan --work-dir "$WORK_TMP" 2>&1 | tee "$WORK_TMP/plan-edited.out"
  if ! grep -q "^Plan: 0 to add, 1 to change, 0 to delete\\.$" "$WORK_TMP/plan-edited.out"; then
    fail "plan after edit should report 1 change"
  fi
  EDIT_SLUG=$(basename "$EDIT_FILE" .yaml)
  if ! grep -q "~ $EDIT_SLUG" "$WORK_TMP/plan-edited.out"; then
    fail "plan after edit should name the edited slug ($EDIT_SLUG)"
  fi
  echo "plan: clean → No changes; edit → 1 change naming $EDIT_SLUG"

  log "upload reconcile applies the diff"
  "$BIN" upload --work-dir "$WORK_TMP" --yes 2>&1 | tee "$WORK_TMP/upload-reconcile.out"
  if ! grep -q "^Applied: 0 created, 1 updated, 0 deleted\\.$" "$WORK_TMP/upload-reconcile.out"; then
    fail "upload reconcile should apply 1 change"
  fi
  if ! grep -q "ChangeSet: " "$WORK_TMP/upload-reconcile.out"; then
    fail "upload reconcile should open and name a ChangeSet"
  fi
  if ! grep -q "Updates revertable via:" "$WORK_TMP/upload-reconcile.out"; then
    fail "upload reconcile should print revert command"
  fi

  log "upload reconcile converges (re-run is no-op)"
  "$BIN" upload --work-dir "$WORK_TMP" 2>&1 | tee "$WORK_TMP/upload-converge.out"
  if ! grep -q "^No changes\\.$" "$WORK_TMP/upload-converge.out"; then
    fail "second upload on the same work-dir should be No changes"
  fi
  if grep -q "ChangeSet: " "$WORK_TMP/upload-converge.out"; then
    fail "second upload should not open a ChangeSet (no changes)"
  fi
  echo "upload reconcile: applied 1 change in ChangeSet; second run is no-op"

  log "setup --pull with edited package source surfaces a real diff via upload reconcile"
  EDITED_SRC="$WORK_TMP/example-stack-edited"
  cp -r "$REPO_ROOT/examples/example-stack" "$EDITED_SRC"
  python3 -c "
import sys, yaml
p = sys.argv[1]
with open(p) as f: docs = list(yaml.safe_load_all(f))
for d in docs:
    if isinstance(d, dict) and isinstance(d.get('metadata'), dict):
        labels = d['metadata'].setdefault('labels', {})
        labels['installer-e2e-upgrade-marker'] = 'true'
        break
with open(p, 'w') as f: yaml.safe_dump_all(docs, f, default_flow_style=False, sort_keys=False)
" "$EDITED_SRC/bases/default/deployment.yaml"

  "$BIN" setup --pull "$EDITED_SRC" --work-dir "$WORK_TMP" --non-interactive 2>&1 \
    | tee "$WORK_TMP/setup-upgrade.out"
  # The setup --pull replaced the package atomically; no .upgrade/ stage.
  if [[ -d "$WORK_TMP/.upgrade" ]]; then
    fail "setup --pull should not create .upgrade/ staging"
  fi
  grep -q "installer-e2e-upgrade-marker" "$WORK_TMP/out/manifests"/*.yaml \
    || fail "setup --pull on edited source should produce the new label in rendered output"

  "$BIN" plan --work-dir "$WORK_TMP" 2>&1 | tee "$WORK_TMP/upgrade-plan.out"
  if ! grep -q "to change" "$WORK_TMP/upgrade-plan.out"; then
    fail "plan after setup --pull (edited source) should plan a change"
  fi
  if ! grep -q "installer-e2e-upgrade-marker" "$WORK_TMP/upgrade-plan.out"; then
    fail "upgrade plan should mention the new label"
  fi

  log "upload reconcile applies the upgrade diff"
  "$BIN" upload --work-dir "$WORK_TMP" --yes 2>&1 | tee "$WORK_TMP/upgrade-upload.out"
  if ! grep -qE "^Applied: [0-9]+ created, [1-9][0-9]* updated, [0-9]+ deleted\\.$" "$WORK_TMP/upgrade-upload.out"; then
    fail "upload reconcile after setup --pull should report at least 1 update"
  fi

  log "setup --pull --set-image bumps the image and the override carries forward"
  "$BIN" setup --pull "$EDITED_SRC" --work-dir "$WORK_TMP" --non-interactive \
    --set-image nginxdemos/hello=nginxdemos/hello:plain-text-v2 2>&1 \
    | tee "$WORK_TMP/setup-setimg.out"
  grep -q "plain-text-v2" "$WORK_TMP/out/manifests"/*.yaml \
    || fail "setup --set-image should rewrite the image tag in rendered output"

  "$BIN" upload --work-dir "$WORK_TMP" --yes 2>&1 | tee "$WORK_TMP/upload-setimg.out"
  if ! grep -qE "^Applied: 0 created, 1 updated, 0 deleted\\.$" "$WORK_TMP/upload-setimg.out"; then
    fail "upload reconcile after --set-image should report exactly 1 update (the image bump)"
  fi
  if ! grep -q "plain-text-v2" "$WORK_TMP/upload-setimg.out"; then
    fail "Images footer should reflect the new tag"
  fi

  log "setup re-run WITHOUT --set-image carries the override forward (no-op)"
  "$BIN" setup --pull "$EDITED_SRC" --work-dir "$WORK_TMP" --non-interactive 2>&1 \
    | tee "$WORK_TMP/setup-carry.out"
  grep -q "plain-text-v2" "$WORK_TMP/out/manifests"/*.yaml \
    || fail "subsequent setup (no --set-image) should preserve the override in rendered output"
  "$BIN" upload --work-dir "$WORK_TMP" 2>&1 | tee "$WORK_TMP/upload-carry.out"
  if ! grep -q "^No changes\\.$" "$WORK_TMP/upload-carry.out"; then
    fail "subsequent upload should be No changes (override carried via installer-record)"
  fi
  echo "setup --set-image: bump applied; override round-trips through installer-record"

  log "setup --pull --set-image against a package without images: block fails fast"
  NO_IMG_SRC="$WORK_TMP/example-stack-no-images"
  cp -r "$EDITED_SRC" "$NO_IMG_SRC"
  python3 -c "
import sys, yaml
p = sys.argv[1]
with open(p) as f: d = yaml.safe_load(f)
d.pop('images', None)
with open(p, 'w') as f: yaml.safe_dump(d, f, sort_keys=False)
" "$NO_IMG_SRC/bases/default/kustomization.yaml"
  if "$BIN" setup --pull "$NO_IMG_SRC" --work-dir "$WORK_TMP" --non-interactive \
      --set-image foo=bar:1 2>&1 | tee "$WORK_TMP/setup-noimg.out"; then
    fail "setup --set-image against package without images: block should fail"
  fi
  if ! grep -q "no \`images:\` block" "$WORK_TMP/setup-noimg.out"; then
    fail "preflight error should name the missing images: block"
  fi
  echo "preflight: package without images: block correctly rejected"
fi

log "OK"
