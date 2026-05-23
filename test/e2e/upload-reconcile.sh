#!/usr/bin/env bash
#
# upload-reconcile.sh — end-to-end smoke for `installer upload` against a
# live ConfigHub server, driven by the worker package so the test
# exercises BOTH the standard Unit pathway and the AppConfig pathway
# (render-configmap Invocation + AppConfig Unit + placeholder + Upsert
# link).
#
# Flow:
#
#   setup --pull   — pulls worker, runs collector (writes facts), renders
#   pin image      — edits facts.yaml to a known release tag, re-renders
#                    via `installer render` (bypasses setup's collector,
#                    which would overwrite facts back to :latest)
#   make Target    — creates a Target in its own Space so the upload can
#                    bind Units to it via cross-Space <space>/<target>
#   upload         — first upload: creates Space + Units + installer-
#                    record + AppConfig set (render-configmap Invocation/
#                    AppConfig Unit/placeholder + Upsert link) + cross-Unit
#                    links. Carries the well-known Space labels (Component
#                    via --component, plus Layer/Environment/Region/Owner/Variant),
#                    --space-label/--space-annotation, --unit-label/
#                    --unit-annotation, and --target.
#   metadata check — asserts the Space labels/annotations, the Unit
#                    Package label + PackageVersion + --unit-* pairs, the
#                    cross-Space TargetID annotation and per-Unit binding,
#                    and that the AppConfig Unit has no Target
#   plan (clean)   — No changes
#   add w/o target — reconcile creates a new Unit with NO --target; it
#                    binds to the Target read back from the Space's
#                    TargetID annotation. Re-passes --environment to prove
#                    "set once, update if re-passed" (others preserved)
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
# Full output of every `installer` and `cub` invocation is written to a
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
# A Target lives in its own Space so the first upload can bind Units to it
# via the cross-Space <space>/<target> --target syntax and record the
# resolved TargetID as $SPACE's "TargetID" annotation.
TARGET_SPACE="${SPACE}-targets"
TARGET_SLUG=installer-test-target
TARGET_WORKER_SLUG=installer-test-target-worker

log()    { printf '\n=== %s ===\n' "$*" >&2; }
note()   { printf '    %s\n' "$*" >&2; }
fail()   { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }

# jq helpers: read a single field from a Space/Unit, stripping the quotes
# jq prints around string values (prints "null"/empty when the field is
# absent). Used by the metadata assertions below.
space_jq() { cub space get "$SPACE" -o "jq=$1" 2>/dev/null | tr -d '"'; }
unit_jq()  { cub unit get --space "$SPACE" "$1" -o "jq=$2" 2>/dev/null | tr -d '"'; }

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
  printf '\nClean up the Space (and everything in it — Units, Invocations, Links,\nthe BridgeWorker entity, ChangeSets) plus the Target Space with:\n  cub space delete --recursive %s\n  cub space delete --recursive %s\n' "$SPACE" "$TARGET_SPACE"
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
log "build installer"
( cd "$REPO_ROOT" && go build -o bin/installer ./cmd/installer )
BIN="$REPO_ROOT/bin/installer"

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
#    re-rendering via `installer render` (NOT setup, which would re-run
#    the collector and revert image back to whatever the server
#    reports — :latest, locally). This both demonstrates that .Facts
#    is a supported override point and gives us a stable image string
#    to assert against downstream.
log "pin worker image to $PINNED_IMAGE (edit facts.yaml + installer render)"
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

# 5c. Create the cross-Space Target the first upload will bind Units to.
#     It needs no live cluster — upload only binds Units (sets TargetID),
#     it never applies — so a backing worker entity that merely *declares*
#     Kubernetes/YAML support (no running process) plus a default
#     Kubernetes Target with empty parameters is enough. Resolving its
#     UUID up front lets later steps assert the recorded "TargetID"
#     annotation and the per-Unit bindings.
log "create cross-Space Target ($TARGET_SPACE/$TARGET_SLUG)"
cub space create --quiet "$TARGET_SPACE" >/dev/null || fail "failed to create Target Space $TARGET_SPACE"
# A Target needs a BridgeWorker that advertises the ConfigType. We don't
# run a worker here (no cluster), so declare the supported ConfigType on
# the worker entity directly via --from-stdin — enough for target-create
# validation and for binding Units (upload binds, it never applies).
printf '%s' '{"ProvidedInfo":{"BridgeWorkerInfo":{"SupportedConfigTypes":[{"ProviderType":"Kubernetes","ToolchainType":"Kubernetes/YAML"}]}}}' \
  | cub worker create --space "$TARGET_SPACE" --from-stdin "$TARGET_WORKER_SLUG" >/dev/null \
  || fail "failed to create Target worker $TARGET_SPACE/$TARGET_WORKER_SLUG"
cub target create --space "$TARGET_SPACE" "$TARGET_SLUG" '{}' "$TARGET_WORKER_SLUG" >/dev/null \
  || fail "failed to create Target $TARGET_SPACE/$TARGET_SLUG"
TARGET_ID=$(cub target get --space "$TARGET_SPACE" "$TARGET_SLUG" -o jq=.Target.TargetID 2>/dev/null | tr -d '"')
[[ -n "$TARGET_ID" && "$TARGET_ID" != "null" ]] || fail "could not resolve TargetID for $TARGET_SPACE/$TARGET_SLUG"
note "Target $TARGET_SPACE/$TARGET_SLUG → TargetID $TARGET_ID"

# 6. First upload — creates Units + AppConfig artifacts, carrying the
#    well-known Space labels (Component overridden via --component), the
#    free-form --space-label / --space-annotation pairs, the --unit-label
#    / --unit-annotation pairs on every Unit, and binding Units to the
#    cross-Space Target (whose TargetID is recorded as a Space annotation).
log "installer upload --space $SPACE (first upload — exercises AppConfig pathway + Space/Unit metadata + cross-Space --target)"
run upload-first "$BIN" upload --work-dir "$WORK_TMP" --space "$SPACE" \
  --component my-component \
  --layer App \
  --environment Prod \
  --region us-east1 \
  --owner Engineering \
  --variant Base \
  --space-label tier=infra \
  --space-annotation note=e2e \
  --unit-label managed-by=installer-e2e \
  --unit-annotation install-note=hello \
  --target "$TARGET_SPACE/$TARGET_SLUG" \
  || fail "first upload failed (see $WORK_TMP/upload-first.log)"

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

# 6c. Metadata assertions: the well-known Space labels, the free-form
#     --space-label / --space-annotation pairs, the --unit-* pairs and the
#     Package/PackageVersion the installer owns, and the --target →
#     TargetID round-trip (Space annotation + per-Unit binding).
log "metadata assertions: Space labels/annotations, Unit labels/annotations, TargetID round-trip"
[[ "$(space_jq .Space.Labels.Component)"   == "my-component" ]] || fail "Space label Component != my-component (got '$(space_jq .Space.Labels.Component)')"
[[ "$(space_jq .Space.Labels.Layer)"       == "App" ]]          || fail "Space label Layer != App"
[[ "$(space_jq .Space.Labels.Environment)" == "Prod" ]]         || fail "Space label Environment != Prod"
[[ "$(space_jq .Space.Labels.Region)"      == "us-east1" ]]     || fail "Space label Region != us-east1"
[[ "$(space_jq .Space.Labels.Owner)"       == "Engineering" ]]  || fail "Space label Owner != Engineering"
[[ "$(space_jq .Space.Labels.Variant)"     == "Base" ]]         || fail "Space label Variant != Base"
[[ "$(space_jq .Space.Labels.tier)"        == "infra" ]]        || fail "Space label tier != infra (--space-label)"
[[ "$(space_jq '.Space.Annotations.note')" == "e2e" ]]          || fail "Space annotation note != e2e (--space-annotation)"
[[ "$(space_jq '.Space.Annotations.TargetID')" == "$TARGET_ID" ]] || fail "Space TargetID annotation != $TARGET_ID (got '$(space_jq '.Space.Annotations.TargetID')')"
note "Space carries Component=my-component + Layer/Environment/Region/Owner/Variant + tier + note + TargetID=$TARGET_ID"

# A standard (non-AppConfig) Unit carries the Package label, the
# PackageVersion annotation, the --unit-* pairs, and the Target binding.
META_UNIT=$(cub unit list --space "$SPACE" 2>/dev/null | awk 'NR>1 && /^deployment-/ {print $1; exit}')
[[ -n "$META_UNIT" ]] || fail "no deployment Unit to check metadata on"
[[ "$(unit_jq "$META_UNIT" .Unit.Labels.Package)" == "confighub-worker" ]] || fail "Unit $META_UNIT Package label != confighub-worker"
[[ "$(unit_jq "$META_UNIT" '.Unit.Labels["managed-by"]')" == "installer-e2e" ]] || fail "Unit $META_UNIT managed-by label != installer-e2e (--unit-label)"
pv=$(unit_jq "$META_UNIT" .Unit.Annotations.PackageVersion); [[ -n "$pv" && "$pv" != "null" ]] || fail "Unit $META_UNIT missing PackageVersion annotation"
[[ "$(unit_jq "$META_UNIT" '.Unit.Annotations["install-note"]')" == "hello" ]] || fail "Unit $META_UNIT install-note annotation != hello (--unit-annotation)"
[[ "$(unit_jq "$META_UNIT" .Unit.TargetID)" == "$TARGET_ID" ]] || fail "Unit $META_UNIT TargetID != $TARGET_ID (cross-Space --target binding)"
note "Unit $META_UNIT: Package=confighub-worker, managed-by=installer-e2e, PackageVersion=$pv, install-note=hello, TargetID=$TARGET_ID"

# The AppConfig Unit is a pure data source → it must NOT be bound to a Target.
appcfg_tid=$(unit_jq confighub-worker-env .Unit.TargetID)
[[ -z "$appcfg_tid" || "$appcfg_tid" == "null" ]] || fail "AppConfig Unit confighub-worker-env should have no Target, got '$appcfg_tid'"
note "AppConfig Unit confighub-worker-env has no Target (pure data source)"

# 7. plan against unchanged work-dir → No changes.
log "installer plan (clean) — expect No changes"
run plan-clean "$BIN" plan --work-dir "$WORK_TMP" || fail "plan failed (see $WORK_TMP/plan-clean.log)"
grep -q "^No changes\\.$" "$WORK_TMP/plan-clean.log" \
  || fail "plan against just-uploaded work-dir should report No changes (see $WORK_TMP/plan-clean.log)"

# 8. AppConfig round-trip FIRST (before the Deployment-edit step):
#    change a real value in the env carrier locally, re-render via
#    installer render (NOT setup — which would re-run the collector and
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
log "installer upload (no changes after AppConfig edit) — re-run is a no-op"
run upload-converge-1 "$BIN" upload --work-dir "$WORK_TMP" || fail "converge upload failed (see $WORK_TMP/upload-converge-1.log)"
grep -q "^No changes\\.$" "$WORK_TMP/upload-converge-1.log" \
  || fail "upload on the same work-dir after AppConfig reconcile should be No changes (see $WORK_TMP/upload-converge-1.log)"

# 9b. Reconcile an ADD without --target: the new Unit must bind to the
#     Target read back from the Space's TargetID annotation. Re-pass ONE
#     well-known label (--environment) with a new value to prove
#     "set once, update if re-passed": Environment updates while the
#     others (not re-passed) and the TargetID annotation are preserved.
#     The manifest is dropped into out/manifests directly (no re-render,
#     so it survives into the reconcile dry-run like the marker edit below).
log "reconcile add without --target: binds from Space TargetID annotation; --environment re-pass updates only Environment"
EXTRA_SLUG=extra-e2e-config
cat > "$WORK_TMP/out/manifests/$EXTRA_SLUG.yaml" <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: $EXTRA_SLUG
  namespace: $SPACE
data:
  hello: world
EOF

run upload-readback "$BIN" upload --work-dir "$WORK_TMP" --yes --environment Staging \
  || fail "reconcile add without --target failed (see $WORK_TMP/upload-readback.log)"
grep -qE "^Applied: 1 created, 0 updated, 0 emptied\\.$" "$WORK_TMP/upload-readback.log" \
  || fail "reconcile add should report exactly 1 created (see $WORK_TMP/upload-readback.log)"

# The added Unit must be bound to the Target read back from the annotation.
extra_tid=$(unit_jq "$EXTRA_SLUG" .Unit.TargetID)
[[ "$extra_tid" == "$TARGET_ID" ]] || fail "added Unit $EXTRA_SLUG TargetID = '$extra_tid', want $TARGET_ID (read back from Space annotation, no --target passed)"
note "added Unit $EXTRA_SLUG bound to TargetID $TARGET_ID without --target (read back from Space annotation)"

# set-once: Environment updated; Component/Layer untouched; TargetID preserved.
[[ "$(space_jq .Space.Labels.Environment)" == "Staging" ]]       || fail "Environment label != Staging (re-passed value should update)"
[[ "$(space_jq .Space.Labels.Component)"   == "my-component" ]]  || fail "Component label changed; should be set-once (not re-passed)"
[[ "$(space_jq .Space.Labels.Layer)"       == "App" ]]           || fail "Layer label changed; should be set-once (not re-passed)"
[[ "$(space_jq .Space.Labels.Region)"      == "us-east1" ]]      || fail "Region label changed; should be set-once (not re-passed)"
[[ "$(space_jq '.Space.Annotations.TargetID')" == "$TARGET_ID" ]] || fail "TargetID annotation changed across reconcile without --target"
note "set-once verified: Environment→Staging; Component/Layer/Region + TargetID preserved"

# 10. Edit a rendered Kubernetes manifest (the Deployment) → plan
#     surfaces the diff. This exercises the standard (non-AppConfig)
#     update path. The marker is injected into out/manifests/ AFTER
#     render, so a subsequent installer render would strip it — but we
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
log "installer upload (reconcile Deployment marker) — applies 1 change"
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
log "installer upload (no changes after Deployment edit) — re-run is a no-op"
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
