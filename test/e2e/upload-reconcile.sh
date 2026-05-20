#!/usr/bin/env bash
#
# upload-reconcile.sh — end-to-end smoke for `install upload` against a
# live ConfigHub server, driven by the worker package so the test
# exercises BOTH the standard Unit pathway and the AppConfig pathway
# (render-configmap Invocation + AppConfig Unit + placeholder + Upsert
# link).
#
# Flow:
#
#   setup --pull   — pulls worker, runs collector (writes facts), renders
#   pin image      — edits facts.yaml to a known release tag, re-renders
#                    via `install render` (bypasses setup's collector,
#                    which would overwrite facts back to :latest)
#   upload         — first upload: creates Space + Units + installer-
#                    record + AppConfig set (render-configmap Invocation/
#                    AppConfig Unit/placeholder + Upsert link) + cross-Unit
#                    links
#   plan (clean)   — No changes
#   edit + plan    — surfaces the edited slug
#   reconcile      — applies inside a ChangeSet
#   re-run         — second upload is a no-op (no ChangeSet)
#   AppConfig edit — edits the .env carrier, re-renders, reconciles —
#                    proves the AppConfig pathway round-trips
#   drop manifest  — removes a rendered manifest so its Unit falls out of
#                    the rendered set; reconcile EMPTIES the Unit (via
#                    merge-external-source), never `cub unit delete`, and
#                    refuses outright while the Unit carries a DestroyGate
#
# Unlike package-and-deps.sh, this test does NOT delete the destination
# Space on exit — the resulting Space is left for manual inspection.
# Clean up afterward with:
#
#   cub space delete --recursive <space-slug>
#
# (Never deletes the `default` Space.)
#
# Full output of every `install` and `cub` invocation is written to a
# per-step log file under the work-dir so debugging a failure does not
# require re-running the (slow) flow.
#
# Configuration via env vars:
#   INSTALLER_UPLOAD_SPACE  — override the destination Space slug.
#                              Default: installer-test-upload-<YYYYMMDD-HHMMSS>.
#   INSTALLER_UPLOAD_IMAGE  — override the pinned worker image.
#                              Default: ghcr.io/confighubai/confighub-worker:v0.1.44.
#                              Local servers report :latest from
#                              `cub worker get-image` (no release running),
#                              so the test pins to a known release for a
#                              stable rendered image string.
#   INSTALLER_UPLOAD_KEEP_WD=0
#                            — also delete the local work-dir on success
#                              (default 1: keep work-dir for inspection).
#   INSTALLER_UPLOAD_VERBOSE=1
#                            — also mirror every step's output to stderr
#                              while running. Without it, only the
#                              per-step log filenames + summary appear
#                              on stderr.
#
# Requirements:
#   - go and a working build environment
#   - kustomize on PATH
#   - cub on PATH, authenticated against the server you want to test

set -euo pipefail

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK_TMP=
SPACE=${INSTALLER_UPLOAD_SPACE:-installer-test-upload-$(date -u +%Y%m%d-%H%M%S)}
KEEP_WD=${INSTALLER_UPLOAD_KEEP_WD:-1}
VERBOSE=${INSTALLER_UPLOAD_VERBOSE:-0}
WORKER_SLUG=installer-test-worker
PINNED_IMAGE=${INSTALLER_UPLOAD_IMAGE:-ghcr.io/confighubai/confighub-worker:v0.1.44}

log()    { printf '\n=== %s ===\n' "$*" >&2; }
note()   { printf '    %s\n' "$*" >&2; }
fail()   { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# run <log-name> <cmd> [args...]
# Runs the command, redirecting both stdout and stderr to
# $WORK_TMP/<log-name>.log. With INSTALLER_UPLOAD_VERBOSE=1 mirrors the
# log to the terminal via tee as well. Returns the command's exit code.
run() {
  local logname=$1
  shift
  local logpath="$WORK_TMP/$logname.log"
  note "→ $logname.log: $*"
  if [[ "$VERBOSE" = "1" ]]; then
    "$@" 2>&1 | tee "$logpath"
    return ${PIPESTATUS[0]}
  fi
  "$@" >"$logpath" 2>&1
}

# Guard: refuse to operate on the default Space, ever — even if the
# user overrides INSTALLER_UPLOAD_SPACE.
if [[ "$SPACE" = "default" ]]; then
  fail "INSTALLER_UPLOAD_SPACE must not be 'default'"
fi

cleanup_msg() {
  printf '\n----- inspection state preserved -----\n'
  printf 'Space:    %s\n' "$SPACE"
  printf 'Work-dir: %s\n' "$WORK_TMP"
  printf '\nClean up the Space (and everything in it — Units, Invocations, Links,\nthe BridgeWorker entity, ChangeSets) with:\n  cub space delete --recursive %s\n' "$SPACE"
  if [[ "$KEEP_WD" = "1" ]]; then
    printf 'Clean up the work-dir with:\n  rm -rf %s\n' "$WORK_TMP"
  fi
  printf '\n'
}

cleanup_on_exit() {
  rc=$?
  set +e
  if [[ "$KEEP_WD" != "1" && $rc = 0 && -n "$WORK_TMP" ]]; then
    rm -rf "$WORK_TMP"
  fi
  if [[ -n "$WORK_TMP" ]]; then
    cleanup_msg
  fi
  exit $rc
}
trap cleanup_on_exit EXIT

# 1. Preflight.
log "preflight"
command -v cub >/dev/null 2>&1 || fail "cub not on PATH"
cub space list >/dev/null 2>&1 || fail "cub auth not configured (run \`cub auth login\`)"
command -v kustomize >/dev/null 2>&1 || fail "kustomize not on PATH"
note "cub OK, kustomize OK"
note "destination Space: $SPACE"
note "BridgeWorker slug: $WORKER_SLUG (created in the same Space for clean teardown)"
note "pinned image:      $PINNED_IMAGE"

# Refuse to clobber an existing Space (so re-runs don't accidentally
# pile state on top of an old one). The user can either pick a new
# slug via INSTALLER_UPLOAD_SPACE or delete the old one first.
if cub space list 2>/dev/null | awk '{print $1}' | grep -qx "$SPACE"; then
  fail "Space '$SPACE' already exists. Delete it first (cub space delete --recursive $SPACE) or set INSTALLER_UPLOAD_SPACE to a fresh slug."
fi

# 2. Build.
log "build install"
( cd "$REPO_ROOT" && go build -o bin/install ./cmd/installer )
BIN="$REPO_ROOT/bin/install"

# 3. Pre-create the destination Space so the worker collector can
#    create the BridgeWorker entity in it (--input space=$SPACE).
#    Upload's auto-Space-create would otherwise race with the collector,
#    which runs during setup BEFORE upload.
log "pre-create destination Space"
cub space create --quiet "$SPACE" >/dev/null

# 4. setup --pull against the local worker package. The worker package
#    declares an AppConfig/Env configMapGenerator (confighub-worker-env),
#    so upload will exercise the AppConfig pathway in addition to the
#    standard Unit pathway. The collector runs cub against the active
#    context to create the BridgeWorker and populate facts.
WORK_TMP=$(mktemp -d -t installer-upload-e2e.XXXXXX)
log "setup --pull (worker package, exercises AppConfig)"
run setup "$BIN" setup --pull "$REPO_ROOT/packages/worker" \
  --work-dir "$WORK_TMP" \
  --non-interactive \
  --namespace "$SPACE" \
  --input worker_slug="$WORKER_SLUG" \
  --input space="$SPACE" || fail "setup failed (see $WORK_TMP/setup.log)"

[[ -d "$WORK_TMP/out/manifests" ]] || fail "expected $WORK_TMP/out/manifests/"
[[ -f "$WORK_TMP/out/spec/facts.yaml" ]] || fail "expected facts.yaml after setup (worker collector should have run)"

# The worker package's secretGenerator emits the worker secret to
# out/secrets/ (sensitive — never uploaded as a Unit).
[[ -d "$WORK_TMP/out/secrets" ]] || fail "expected $WORK_TMP/out/secrets/ (rendered Secret routed off the upload path)"

# 5. Pin the image to a known release tag by editing facts.yaml and
#    re-rendering via `install render` (NOT setup, which would re-run
#    the collector and revert image back to whatever the server
#    reports — :latest, locally). This both demonstrates that .Facts
#    is a supported override point and gives us a stable image string
#    to assert against downstream.
log "pin worker image to $PINNED_IMAGE (edit facts.yaml + install render)"
note "collector-reported image was:"
grep -E '^[[:space:]]+image:' "$WORK_TMP/out/spec/facts.yaml" | sed 's/^/      /'
# Use python for a safe in-place YAML update — preserves other fact keys.
python3 - "$WORK_TMP/out/spec/facts.yaml" "$PINNED_IMAGE" <<'PY'
import sys, yaml
p, image = sys.argv[1], sys.argv[2]
with open(p) as f:
    doc = yaml.safe_load(f)
doc.setdefault('spec', {}).setdefault('values', {})['image'] = image
with open(p, 'w') as f:
    yaml.safe_dump(doc, f, sort_keys=False)
PY

run render-pinned "$BIN" render --work-dir "$WORK_TMP" || fail "render after facts edit failed (see $WORK_TMP/render-pinned.log)"

# Assert the rendered Deployment now references the pinned image.
DEP_FILE=$(ls "$WORK_TMP/out/manifests"/deployment-*.yaml 2>/dev/null | head -1)
[[ -n "$DEP_FILE" ]] || fail "no rendered Deployment manifest"
grep -q "image: $PINNED_IMAGE" "$DEP_FILE" \
  || fail "rendered Deployment $(basename "$DEP_FILE") does not reference pinned image $PINNED_IMAGE"
note "rendered Deployment now references $PINNED_IMAGE"

# The AppConfig ConfigMap (confighub-worker-env) should be present in
# rendered manifests — upload will split it into a render-configmap
# Invocation + AppConfig Unit + placeholder.
appcfg_cm=$(grep -l "installer.confighub.com/toolchain: AppConfig/Env" "$WORK_TMP/out/manifests"/*.yaml | head -1)
[[ -n "$appcfg_cm" ]] || fail "expected at least one AppConfig-tagged ConfigMap among rendered manifests"
note "AppConfig carrier ConfigMap: $(basename "$appcfg_cm")"

# Confirm the BridgeWorker entity was created by the collector during setup.
cub worker list --space "$SPACE" 2>/dev/null | awk '{print $1}' | grep -qx "$WORKER_SLUG" \
  || fail "collector did not create BridgeWorker '$WORKER_SLUG' in Space $SPACE"

# 6. First upload — creates Units + AppConfig artifacts.
log "install upload --space $SPACE (first upload — exercises AppConfig pathway)"
run upload-first "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" || fail "first upload failed (see $WORK_TMP/upload-first.log)"

[[ -f "$WORK_TMP/out/spec/upload.yaml" ]] || fail "first upload did not write out/spec/upload.yaml"

# 6a. Standard-Unit assertions.
unit_count=$(cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1' | wc -l | tr -d ' ')
note "Units in $SPACE after first upload: $unit_count"
[[ "$unit_count" -ge 5 ]] || fail "expected at least 5 Units in $SPACE, got $unit_count"

cub unit list --space "$SPACE" 2>/dev/null | awk '{print $1}' | grep -qx "installer-record" \
  || fail "first upload did not create installer-record Unit in $SPACE"

# 6b. AppConfig-pathway assertions: one render-configmap Invocation and
# one *-rendered placeholder Unit. No bridge Target and no renderer
# worker — the rendering happens via the render-configmap function on an
# Upsert link.
invocation_count=$(cub invocation list --space "$SPACE" 2>/dev/null | awk 'NR>1 && /-render/' | wc -l | tr -d ' ')
[[ "$invocation_count" -ge 1 ]] || fail "expected at least one render-configmap Invocation in $SPACE (got $invocation_count)"
note "render-configmap Invocations:"
cub invocation list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}' || true

placeholder_count=$(cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1 && /-rendered/' | wc -l | tr -d ' ')
[[ "$placeholder_count" -ge 1 ]] || fail "expected at least one *-rendered placeholder Unit in $SPACE"

# The placeholder should hold a rendered ConfigMap (the Upsert link ran).
cub unit data --space "$SPACE" confighub-worker-env-rendered 2>/dev/null | grep -q "kind: ConfigMap" \
  || fail "placeholder confighub-worker-env-rendered should contain a rendered ConfigMap"
note "rendered ConfigMap present in placeholder Unit"

link_count=$(cub link list --space "$SPACE" 2>/dev/null | awk 'NR>1' | wc -l | tr -d ' ')
[[ "$link_count" -ge 1 ]] || fail "expected at least one intra-Space link in $SPACE"
note "Links in $SPACE: $link_count"

note "Units in $SPACE:"
cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}'

# 7. plan against unchanged work-dir → No changes.
log "install plan (clean) — expect No changes"
run plan-clean "$BIN" plan --work-dir "$WORK_TMP" || fail "plan failed (see $WORK_TMP/plan-clean.log)"
grep -q "^No changes\\.$" "$WORK_TMP/plan-clean.log" \
  || fail "plan against just-uploaded work-dir should report No changes (see $WORK_TMP/plan-clean.log)"

# 8. AppConfig round-trip FIRST (before the Deployment-edit step):
#    change a real value in the env carrier locally, re-render via
#    install render (NOT setup — which would re-run the collector and
#    revert the pinned image), then reconcile via upload. Proves the
#    AppConfig pathway picks up source-format changes AND that the
#    diff's AppConfig-aware path (UnitSlug detection + raw-content
#    merge) routes the change to the AppConfig Unit, not the rendered
#    placeholder.
#
# A comment edit doesn't surface — AppConfig/Env normalizes the body
# before computing a diff — so flip a concrete value instead.
log "AppConfig round-trip: edit the env carrier + render + reconcile"
appcfg_in_pkg="$WORK_TMP/package/bases/default/confighub-worker.env"
[[ -f "$appcfg_in_pkg" ]] || fail "expected $appcfg_in_pkg in pulled worker package"
sed -i.bak 's/^CONFIGHUB_WORKER_HTTP_SERVER_PORT=.*/CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093/' "$appcfg_in_pkg"
rm -f "$appcfg_in_pkg.bak"
grep -q '^CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093$' "$appcfg_in_pkg" \
  || fail "failed to flip port in $appcfg_in_pkg"

run render-appcfg "$BIN" render --work-dir "$WORK_TMP" || fail "render after AppConfig edit failed (see $WORK_TMP/render-appcfg.log)"

# Plan should surface exactly 1 change on the AppConfig Unit slug
# (confighub-worker-env), not on the *-rendered placeholder.
run plan-appcfg "$BIN" plan --work-dir "$WORK_TMP" || fail "plan after AppConfig edit failed (see $WORK_TMP/plan-appcfg.log)"
grep -q "^Plan: 0 to add, 1 to change, 0 to delete\\.$" "$WORK_TMP/plan-appcfg.log" \
  || fail "plan after AppConfig edit should report exactly 1 change (see $WORK_TMP/plan-appcfg.log)"
grep -q "~ confighub-worker-env\b" "$WORK_TMP/plan-appcfg.log" \
  || fail "plan after AppConfig edit should name the AppConfig Unit slug confighub-worker-env (see $WORK_TMP/plan-appcfg.log)"
if grep -q "~ confighub-worker-env-rendered" "$WORK_TMP/plan-appcfg.log"; then
  fail "plan should NOT name the *-rendered placeholder (it's maintained by the Upsert link, not by reconcile)"
fi

run upload-appcfg "$BIN" upload --work-dir "$WORK_TMP" --yes || fail "upload after AppConfig edit failed (see $WORK_TMP/upload-appcfg.log)"
grep -qE "^Applied: 0 created, 1 updated, 0 emptied\\.$" "$WORK_TMP/upload-appcfg.log" \
  || fail "upload reconcile after AppConfig edit should report exactly 1 update (see $WORK_TMP/upload-appcfg.log)"

# Verify the AppConfig Unit body on the server actually has the new
# value — proves the merge-external-source path used the raw env
# content (not the rendered ConfigMap manifest).
cub unit data --space "$SPACE" confighub-worker-env > "$WORK_TMP/confighub-worker-env.after.txt"
grep -q "^CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093$" "$WORK_TMP/confighub-worker-env.after.txt" \
  || fail "AppConfig Unit confighub-worker-env did not pick up the new PORT (see $WORK_TMP/confighub-worker-env.after.txt)"
note "AppConfig Unit body now has CONFIGHUB_WORKER_HTTP_SERVER_PORT=9093"

# 9. Second upload — converges, no ChangeSet opened.
log "install upload (no changes after AppConfig edit) — re-run is a no-op"
run upload-converge-1 "$BIN" upload --work-dir "$WORK_TMP" || fail "converge upload failed (see $WORK_TMP/upload-converge-1.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-converge-1.log" \
  || fail "upload on the same work-dir after AppConfig reconcile should be No changes (see $WORK_TMP/upload-converge-1.log)"

# 10. Edit a rendered Kubernetes manifest (the Deployment) → plan
#     surfaces the diff. This exercises the standard (non-AppConfig)
#     update path. The marker is injected into out/manifests/ AFTER
#     render, so a subsequent install render would strip it — but we
#     don't re-render here, so the marker stays for the reconcile
#     dry-run.
log "edit rendered Deployment, plan surfaces diff"
EDIT_FILE="$DEP_FILE"
python3 - "$EDIT_FILE" <<'PY'
import sys, yaml
p = sys.argv[1]
with open(p) as f:
    docs = list(yaml.safe_load_all(f))
for d in docs:
    if isinstance(d, dict) and isinstance(d.get('metadata'), dict):
        labels = d['metadata'].setdefault('labels', {})
        labels['installer-test-marker'] = 'true'
        break
with open(p, 'w') as f:
    yaml.safe_dump_all(docs, f, default_flow_style=False, sort_keys=False)
PY
EDIT_SLUG=$(basename "$EDIT_FILE" .yaml)
note "marker label injected into $EDIT_SLUG"

run plan-edited "$BIN" plan --work-dir "$WORK_TMP" || fail "plan after edit failed (see $WORK_TMP/plan-edited.log)"
grep -q "^Plan: 0 to add, 1 to change, 0 to delete\\.$" "$WORK_TMP/plan-edited.log" \
  || fail "plan after edit should report exactly 1 change (see $WORK_TMP/plan-edited.log)"
grep -q "~ $EDIT_SLUG" "$WORK_TMP/plan-edited.log" \
  || fail "plan after edit should name the edited slug ($EDIT_SLUG) (see $WORK_TMP/plan-edited.log)"

# 11. upload reconcile — applies the diff inside a ChangeSet.
log "install upload (reconcile Deployment marker) — applies 1 change"
run upload-reconcile "$BIN" upload --work-dir "$WORK_TMP" --yes || fail "upload reconcile failed (see $WORK_TMP/upload-reconcile.log)"
grep -q "^Applied: 0 created, 1 updated, 0 emptied\\.$" "$WORK_TMP/upload-reconcile.log" \
  || fail "upload reconcile should apply 1 change (see $WORK_TMP/upload-reconcile.log)"
grep -q "ChangeSet: " "$WORK_TMP/upload-reconcile.log" \
  || fail "upload reconcile should open and name a ChangeSet"
grep -q "Updates revertable via:" "$WORK_TMP/upload-reconcile.log" \
  || fail "upload reconcile should print revert command"

CHANGESET=$(grep -m1 "ChangeSet: " "$WORK_TMP/upload-reconcile.log" | awk -F'/' '{print $NF}' | sed -E 's| .*$||')
note "ChangeSet opened: $SPACE/$CHANGESET"

# Cross-check: HEAD revision exists for the edited slug, and the unit
# body has the marker label we injected.
cub unit data --space "$SPACE" "$EDIT_SLUG" > "$WORK_TMP/deployment.after.txt"
grep -q "installer-test-marker: 'true'" "$WORK_TMP/deployment.after.txt" \
  || fail "Deployment Unit body on the server is missing the injected marker label (see $WORK_TMP/deployment.after.txt)"
note "Deployment Unit body now has the installer-test-marker label"

# 12. Second upload — converges, no ChangeSet opened.
log "install upload (no changes after Deployment edit) — re-run is a no-op"
run upload-converge-2 "$BIN" upload --work-dir "$WORK_TMP" || fail "converge upload failed (see $WORK_TMP/upload-converge-2.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-converge-2.log" \
  || fail "upload on the same work-dir after Deployment reconcile should be No changes (see $WORK_TMP/upload-converge-2.log)"
if grep -q "ChangeSet: " "$WORK_TMP/upload-converge-2.log"; then
  fail "converge upload should not open a ChangeSet (no changes)"
fi

# 13. Resource deletion: drop a rendered manifest so its Unit falls out
#     of the rendered set. Reconcile must EMPTY the Unit (via `cub unit
#     update --merge-external-source` with empty content) — never `cub
#     unit delete`. The Unit record, target binding, and metadata must
#     survive so the next apply can remove the deployed resources. The
#     ClusterRoleBinding is a safe leaf to drop (cluster-scoped, nothing
#     Needs it).
log "resource deletion: drop a rendered manifest, reconcile empties (not deletes) the Unit"
DEL_FILE=$(ls "$WORK_TMP/out/manifests"/clusterrolebinding-*.yaml 2>/dev/null | head -1)
[[ -n "$DEL_FILE" ]] || fail "no rendered ClusterRoleBinding manifest to delete"
DEL_SLUG=$(basename "$DEL_FILE" .yaml)
note "dropping rendered manifest for slug $DEL_SLUG"
rm -f "$DEL_FILE"

# Plan surfaces exactly one delete, naming the slug under "-".
run plan-deleted "$BIN" plan --work-dir "$WORK_TMP" || fail "plan after manifest drop failed (see $WORK_TMP/plan-deleted.log)"
grep -q "^Plan: 0 to add, 0 to change, 1 to delete\\.$" "$WORK_TMP/plan-deleted.log" \
  || fail "plan after manifest drop should report exactly 1 delete (see $WORK_TMP/plan-deleted.log)"
grep -q "^  - $DEL_SLUG$" "$WORK_TMP/plan-deleted.log" \
  || fail "plan after manifest drop should list '  - $DEL_SLUG' (see $WORK_TMP/plan-deleted.log)"

# 13a. DestroyGate refusal: a Unit guarded by a DestroyGate must NOT be
#      emptied (emptying + apply would destroy its deployed resources).
log "DestroyGate refusal: gated Unit must not be emptied"
cub unit update --patch --space "$SPACE" --destroy-gate "installer-e2e" "$DEL_SLUG" >/dev/null \
  || fail "failed to set DestroyGate on $DEL_SLUG"
if run upload-gated "$BIN" upload --work-dir "$WORK_TMP" --yes; then
  fail "upload must refuse to empty $DEL_SLUG while it carries a DestroyGate (see $WORK_TMP/upload-gated.log)"
fi
grep -q "guarded by DestroyGates" "$WORK_TMP/upload-gated.log" \
  || fail "upload refusal should mention DestroyGates (see $WORK_TMP/upload-gated.log)"
cub unit data --space "$SPACE" "$DEL_SLUG" 2>/dev/null | grep -q "kind: ClusterRoleBinding" \
  || fail "gated Unit $DEL_SLUG must remain intact (still a ClusterRoleBinding) after refusal"
note "gated Unit $DEL_SLUG left untouched"

# 13b. Clear the gate, reconcile for real: the Unit is emptied, not deleted.
log "clear the gate, reconcile empties the Unit"
cub unit update --patch --space "$SPACE" --destroy-gate "installer-e2e=-" "$DEL_SLUG" >/dev/null \
  || fail "failed to remove DestroyGate from $DEL_SLUG"
run upload-emptied "$BIN" upload --work-dir "$WORK_TMP" --yes || fail "upload empty failed (see $WORK_TMP/upload-emptied.log)"
grep -qE "^Applied: 0 created, 0 updated, 1 emptied\\.$" "$WORK_TMP/upload-emptied.log" \
  || fail "upload should report exactly 1 emptied (see $WORK_TMP/upload-emptied.log)"
grep -q "Emptied $SPACE/$DEL_SLUG" "$WORK_TMP/upload-emptied.log" \
  || fail "upload should print 'Emptied $SPACE/$DEL_SLUG' (see $WORK_TMP/upload-emptied.log)"

# The Unit record must still exist (no cub unit delete) and its Data must
# no longer contain the ClusterRoleBinding.
cub unit get --space "$SPACE" "$DEL_SLUG" >/dev/null 2>&1 \
  || fail "emptied Unit $DEL_SLUG must still exist — upload must never run 'cub unit delete'"
if cub unit data --space "$SPACE" "$DEL_SLUG" 2>/dev/null | grep -q "kind: ClusterRoleBinding"; then
  fail "emptied Unit $DEL_SLUG should no longer contain the ClusterRoleBinding"
fi
note "Unit $DEL_SLUG still exists and no longer contains the ClusterRoleBinding"

log "summary"
note "Space:           $SPACE"
note "BridgeWorker:    $WORKER_SLUG (in Space $SPACE)"
note "Worker image:    $PINNED_IMAGE (pinned via facts.yaml override)"
note "Work-dir:        $WORK_TMP"
note "ChangeSet:       $SPACE/$CHANGESET (from the manifest-edit reconcile)"
note ""
note "Final Unit list:"
cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}'
note ""
note "Final Target list:"
cub target list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}' || true
note ""
note "Final Link list:"
cub link list --space "$SPACE" 2>/dev/null | awk 'NR>1 {print "      "$1}' || true

log "OK"
