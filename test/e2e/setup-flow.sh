#!/usr/bin/env bash
#
# setup-flow.sh — end-to-end smoke for the consolidated `installer setup`
# command. Exercises the local pipeline (no ConfigHub required):
#
#   first install via setup --pull
#   re-render via plain setup (no --pull)
#   upgrade via setup --pull <newer> with input schema diff
#   image override carry-forward via setup --set-image
#   plain installer pull writes to <work-dir>/package atomically
#
# Requirements:
#   - go and a working build environment
#   - kustomize on PATH (render shells out to it)
#
# Exit codes:
#   0  pipeline succeeded
#   1  pipeline failed
#
# All temp state lives in $(mktemp -d) and is removed on exit unless
# INSTALLER_E2E_KEEP=1.

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK_TMP=
PKG_V1=
PKG_V2=
KEEP=${INSTALLER_E2E_KEEP:-0}

log() { printf '\n=== %s ===\n' "$*"; }
fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ "$KEEP" != "1" ]]; then
    [[ -n "$WORK_TMP" ]] && rm -rf "$WORK_TMP"
    [[ -n "$PKG_V1"   ]] && rm -rf "$PKG_V1"
    [[ -n "$PKG_V2"   ]] && rm -rf "$PKG_V2"
  else
    echo "preserved: $WORK_TMP $PKG_V1 $PKG_V2"
  fi
}
trap cleanup EXIT

# 1. Build.
log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

# 2. Stage two versions of hello-app to drive an upgrade flow against
#    local paths (no OCI registry needed). v2 adds a new input with a
#    default — should be silently adopted on setup --pull.
PKG_V1=$(mktemp -d -t setup-flow-pkg-v1.XXXXXX)
PKG_V2=$(mktemp -d -t setup-flow-pkg-v2.XXXXXX)
cp -r "$REPO_ROOT/examples/hello-app/." "$PKG_V1/"
cp -r "$REPO_ROOT/examples/hello-app/." "$PKG_V2/"

# Bump the v2 manifest's version + add a new input with a default.
python3 -c "
import sys, yaml
p = sys.argv[1]
with open(p) as f: d = yaml.safe_load(f)
d['metadata']['version'] = '0.2.0'
inputs = d['spec'].setdefault('inputs', [])
inputs.append({
    'name': 'log_level',
    'type': 'string',
    'default': 'info',
    'description': 'Log verbosity (added in v0.2.0)',
})
with open(p, 'w') as f: yaml.safe_dump(d, f, sort_keys=False)
" "$PKG_V2/installer.yaml"

# 3. First install via setup --pull (v1).
WORK_TMP=$(mktemp -d -t setup-flow-wd.XXXXXX)
log "setup --pull (v1) — first install"
"$BIN" setup --pull "$PKG_V1" --work-dir "$WORK_TMP" \
  --non-interactive --namespace setup-test --select monitoring \
  --output-oci "$WORK_TMP/rendered.oci" 2>&1 \
  | tee "$WORK_TMP/setup-v1.out"

[[ -d "$WORK_TMP/package" ]] || fail "expected $WORK_TMP/package/ after setup --pull"
[[ -f "$WORK_TMP/out/spec/selection.yaml" ]] || fail "expected out/spec/selection.yaml"
[[ -f "$WORK_TMP/out/spec/inputs.yaml" ]] || fail "expected out/spec/inputs.yaml"
[[ -d "$WORK_TMP/out/manifests" ]] || fail "expected out/manifests/"
[[ ! -d "$WORK_TMP/.upgrade" ]] || fail "setup should NOT create .upgrade/ (atomic pull)"
[[ ! -d "$WORK_TMP/.upgrade-prev" ]] || fail "setup should NOT create .upgrade-prev/"
[[ -f "$WORK_TMP/rendered.oci/oci-layout" ]] || fail "expected local rendered OCI layout"
[[ -f "$WORK_TMP/rendered.oci/index.json" ]] || fail "expected rendered OCI index"
grep -q "pull-back: verified" "$WORK_TMP/setup-v1.out" \
  || fail "setup should verify the rendered OCI after writing it"

if ! grep -q "Loaded prior install state" "$WORK_TMP/setup-v1.out"; then
  : # fresh install — no prior state expected
fi

# Manifests should contain the monitoring ServiceMonitor.
if ! find "$WORK_TMP/out/manifests" -name 'servicemonitor*' -print -quit | grep -q .; then
  fail "monitoring component should produce a servicemonitor manifest"
fi

# 4. Bare setup (no --pull) — re-render against existing package.
log "setup (no --pull) — re-render is byte-identical"
mkdir -p "$WORK_TMP/manifests.bak" && cp "$WORK_TMP/out/manifests"/*.yaml "$WORK_TMP/manifests.bak/"
"$BIN" setup --work-dir "$WORK_TMP" --non-interactive 2>&1 \
  | tee "$WORK_TMP/setup-rerender.out"
grep -q "Loaded prior install state from local" "$WORK_TMP/setup-rerender.out" \
  || fail "second setup should report loading prior state"
diff -q -r "$WORK_TMP/out/manifests" "$WORK_TMP/manifests.bak" >/dev/null \
  || fail "second setup without --pull should produce byte-identical output"
rm -rf "$WORK_TMP/manifests.bak"

# 5. setup with no --pull and no prior package errors cleanly.
EMPTY_WD=$(mktemp -d -t setup-flow-empty.XXXXXX)
log "setup with no --pull and no package fails fast"
if "$BIN" setup --work-dir "$EMPTY_WD" --non-interactive --namespace x 2>&1 \
    | tee "$EMPTY_WD/err.out"; then
  fail "setup in empty work-dir without --pull should error"
fi
grep -q "no package found" "$EMPTY_WD/err.out" \
  || fail "error should mention 'no package found' and suggest --pull"
rm -rf "$EMPTY_WD"

# 6. setup --pull v2 — upgrade flow with schema-diff (silently adopts
#    the new default for log_level).
log "setup --pull v2 — schema-diff adopts new default"
"$BIN" setup --pull "$PKG_V2" --work-dir "$WORK_TMP" --non-interactive 2>&1 \
  | tee "$WORK_TMP/setup-v2.out"
grep -q 'Adopted new default for input "log_level"' "$WORK_TMP/setup-v2.out" \
  || fail "setup --pull v2 should report adopting the new log_level default"

# Confirm log_level is in inputs.yaml.
grep -q 'log_level: info' "$WORK_TMP/out/spec/inputs.yaml" \
  || fail "inputs.yaml should contain the adopted log_level value"

# 7. setup --pull v2 again with --set-image — image override applies +
#    persists.
log "setup --pull --set-image — override applied"
"$BIN" setup --pull "$PKG_V2" --work-dir "$WORK_TMP" --non-interactive \
  --set-image nginxdemos/hello=nginxdemos/hello:plain-text-v2 2>&1 \
  | tee "$WORK_TMP/setup-setimg.out"
grep -q 'plain-text-v2' "$WORK_TMP/out/manifests"/*.yaml \
  || fail "--set-image should rewrite the image tag in rendered output"
grep -q 'plain-text-v2' "$WORK_TMP/out/spec/inputs.yaml" \
  || fail "image override should be recorded in inputs.yaml"

# 8. setup --pull v2 again WITHOUT --set-image — override carries forward.
log "setup --pull (no --set-image) — override carries forward"
"$BIN" setup --pull "$PKG_V2" --work-dir "$WORK_TMP" --non-interactive 2>&1 \
  | tee "$WORK_TMP/setup-carry.out"
grep -q 'plain-text-v2' "$WORK_TMP/out/manifests"/*.yaml \
  || fail "image override should still be in the rendered manifest after re-pull"

# 9. installer pull --work-dir into a fresh dir.
PULL_WD=$(mktemp -d -t setup-flow-pull.XXXXXX)
log "installer pull --work-dir writes to <work-dir>/package/"
"$BIN" pull "$PKG_V1" --work-dir "$PULL_WD" >/dev/null
[[ -f "$PULL_WD/package/installer.yaml" ]] \
  || fail "installer pull should produce $PULL_WD/package/installer.yaml"
[[ ! -d "$PULL_WD/.tmp-pull-"* ]] \
  || fail "installer pull should not leave .tmp-pull-* staging behind on success"

# 10. installer pull --work-dir on an existing dir replaces the package
#     atomically.
log "installer pull --work-dir replaces existing package/ atomically"
sentinel="$PULL_WD/package/installer.yaml.sentinel"
touch "$sentinel"
"$BIN" pull "$PKG_V2" --work-dir "$PULL_WD" >/dev/null
[[ ! -f "$sentinel" ]] || fail "second pull should fully replace package/ (sentinel still present)"
[[ -f "$PULL_WD/package/installer.yaml" ]] || fail "new package missing installer.yaml"
grep -q 'version: 0.2.0' "$PULL_WD/package/installer.yaml" \
  || fail "second pull should have written v0.2.0 manifest"
rm -rf "$PULL_WD"

log "OK"
