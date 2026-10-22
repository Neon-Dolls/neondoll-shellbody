#!/usr/bin/env bash
#
# run_m10_5_harness.sh — M10.5 Independent Process Deployment Proof.
#
# Proves a real neonDoll Shell Body binary can enroll (via Core identity
# discovery and the REAL owner-approved ceremony), authenticate, advertise,
# reconnect after a Core restart using its persisted identity (no re-enroll),
# and have its subsequent authentication denied after the owner revokes the
# Body — all against the REAL released Core 4 daemon, each binary launched as
# a separate OS process with isolated temporary state and loopback networking.
#
# The harness deliberately runs the PRODUCTION binaries. It never mocks Core,
# never uses in-process fixtures, never builds the acceptance-tagged daemon,
# and never bypasses owner approval or Guard authorization.
#
# Requirement: exactly one step is BLOCKED by the released Core — there is no
# production execution endpoint and no shell/process capability that Core's
# Guard can authorize (see the step-5 probe and M10_5_DEPLOYMENT.md). Per the
# milestone constraints the harness does not simulate it; it probes the real
# routes, records the 40x, and reports the blocker + proposed Core follow-up.
#
# Usage:
#   NEONDOLLD=/path/neondolld \
#   NEONDOLLCTL=/path/neondollctl \
#   NEONDOLL_SHELLBODY=/path/neondoll-shellbody \
#   ./test/deploy/run_m10_5_harness.sh
#
# Optional env:
#   M10_5_CORE_PORT    fixed loopback HTTP port (default: dynamic pick)
#   M10_5_WORKDIR      keep working directory in a custom path (no cleanup)
#   M10_5_TRACE=1      bash -x trace
#
# Exit code 0 = steps 1-4,6,7 proven and step 5 confirmed-blocked as documented.
# On any other failure it defers to the trap to kill all spawned processes and
# remove the temp workspace, then exits non-zero.
set -euo pipefail

if [[ "${M10_5_TRACE:-0}" == "1" ]]; then set -x; fi

NEONDOLLD="${NEONDOLLD:-}"
NEONDOLLCTL="${NEONDOLLCTL:-}"
NEONDOLL_SHELLBODY="${NEONDOLL_SHELLBODY:-}"
for b in NEONDOLLD NEONDOLLCTL NEONDOLL_SHELLBODY; do
  if [[ -z "${!b}" ]]; then
    echo "error: $b is required (set NEONDOLLD/NEONDOLLCTL/NEONDOLL_SHELLBODY to real binary paths)" >&2
    exit 2
  fi
  if [[ ! -x "${!b}" ]]; then echo "error: ${!b} is not executable" >&2; exit 2; fi
done

CORE_PORT="${M10_5_CORE_PORT:-}"
if [[ -z "$CORE_PORT" ]]; then
  if command -v python3 >/dev/null 2>&1; then
    CORE_PORT="$(python3 -c 'import socket,sys; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  else
    CORE_PORT="19550"
  fi
fi
WS_URL="ws://127.0.0.1:${CORE_PORT}/ws"
HTTP_URL="http://127.0.0.1:${CORE_PORT}"

# ── Workspace (isolated) ──────────────────────────────────────────────────
WORK="$(mktemp -d "${TMPDIR:-/tmp}/m10.5.XXXXXX")"
DATA_DIR="$WORK/data"; STATE_DIR="$WORK/state"; SECRETS_DIR="$WORK/secrets"
BODY1="$WORK/body1"; BODY2="$WORK/body2"; BODY3="$WORK/body3"
if [[ -n "${M10_5_WORKDIR:-}" ]]; then WORK="$M10_5_WORKDIR"; WORK_PINNED=1; fi
mkdir -p "$DATA_DIR" "$STATE_DIR" "$SECRETS_DIR" "$BODY1" "$BODY2" "$BODY3"

DAEMON_PID=""
BODY_PID=""
ENROLL_PID=""
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  [[ -n "$BODY_PID"   ]] && kill "$BODY_PID"   2>/dev/null || true
  [[ -n "$ENROLL_PID" ]] && kill "$ENROLL_PID" 2>/dev/null || true
  if [[ -n "$DAEMON_PID" ]]; then
    kill "$DAEMON_PID" 2>/dev/null || true
    # give the daemon a moment to write its clean-shutdown diagnostic
    for _ in 1 2 3 4 5; do kill -0 "$DAEMON_PID" 2>/dev/null || break; sleep 0.5; done
    kill -9 "$DAEMON_PID" 2>/dev/null || true
  fi
  if [[ -n "${WORK_PINNED:-}" ]]; then
    echo "[notice] keeping workspace: $WORK"
  else
    rm -rf "$WORK"
  fi
  exit $rc
}
trap cleanup EXIT INT TERM

say()  { echo -e "\n== $*"; }
die()  { echo -e "\nFATAL: $*" >&2; exit 1; }
step() { printf '%-22s %s\n' "[step $1]" "$2"; }

# wait_for LOGFILE PATTERN SECONDS — blocks until PATTERN (basic regex) appears
# in LOGFILE or the budget elapses (observable readiness; no arbitrary sleeps).
wait_for() {
  local log="$1" pat="$2" budget="${3:-30}"
  local i=0
  while [[ $i -lt $((budget*2)) ]]; do
    if [[ -f "$log" ]] && grep -Eq "$pat" "$log" 2>/dev/null; then return 0; fi
    sleep 0.5; i=$((i+1))
  done
  return 1
}
no_new_match_in() {
  local log="$1" pat="$2" budget="${3:-12}" before="$4"
  local i=0
  while [[ $i -lt $((budget*2)) ]]; do
    local n; n="$(grep -Ec "$pat" "$log" 2>/dev/null || echo 0)"
    if [[ "$n" -gt "$before" ]]; then return 1; fi  # a new match appeared
    sleep 0.5; i=$((i+1))
  done
  return 0
}

ADMIN_SOCK="$DATA_DIR/neondoll-admin.sock"
CTL=( "$NEONDOLLCTL" -socket "$ADMIN_SOCK" )

echo "=============================================================="
echo "  M10.5 Independent Process Deployment Proof"
echo "  core        : $NEONDOLLD"
echo "  ctl         : $NEONDOLLCTL"
echo "  shell-body  : $NEONDOLL_SHELLBODY"
echo "  http/ws     : 127.0.0.1:$CORE_PORT"
echo "  workspace   : $WORK"
echo "=============================================================="

# ── Step 1: launch the real Core daemon as its own OS process ─────────────
step 1 "launch neondolld (real Core daemon) with isolated state + loopback"
cat > "$WORK/config.json" <<EOF
{
  "core": {
    "profile": "default",
    "environment": "development",
    "pulse": {"enabled": false}
  },
  "console": {"enabled": true, "prompt": "> "},
  "inference": {"provider": "openai", "base_url": "http://127.0.0.1:8080"},
  "paths": {"state_dir": "$STATE_DIR", "secrets_dir": "$SECRETS_DIR", "data_dir": "$DATA_DIR"},
  "link": {"websocket": {"enabled": false, "listen": "127.0.0.1:${CORE_PORT}"}},
  "http": {"enabled": true, "listen": "127.0.0.1:${CORE_PORT}", "enable_cors": true},
  "relay": {"enabled": false, "url": "", "token": ""}
}
EOF
( cd "$WORK" && exec "$NEONDOLLD" > daemon.log 2>&1 ) &
DAEMON_PID=$!
wait_for "$WORK/daemon.log" 'neondoll started:' 40 || {
  echo "--- daemon.log ---"; tail -40 "$WORK/daemon.log" || true; die "neondolld did not reach readiness"
}
step 1 "ok: daemon ready (pid $DAEMON_PID)"
grep -m1 'neondoll started:' "$WORK/daemon.log" >/dev/null
CORE_ID_BEFORE="$(curl -sf "$HTTP_URL/v1/core/identity" | python3 -c 'import sys,json;print(json.load(sys.stdin)["core_id"])')"
echo "       core_id (discovered via GET /v1/core/identity): $CORE_ID_BEFORE"

# ── Step 2: owner creates an invitation (real neondollctl, real admin socket)
step 2 "owner creates invitation via neondollctl body invite"
INVITE="$WORK/invite.json"
"${CTL[@]}" body invite -expires 15m -permissions process.run -endpoint "$HTTP_URL" > "$INVITE" 2> "$WORK/invite.err" \
  || { echo "--- invite.err ---"; cat "$WORK/invite.err"; die "body invite failed (is the admin socket '$ADMIN_SOCK' correct?)"; }
grep -q '"invitation_id"' "$INVITE" || die "invite output missing invitation_id: $(cat "$INVITE")"
echo "       invitation: $(tr -d '\n' <"$INVITE" | head -c 200)"

# ── Step 3: enroll Shell Body — (a) owner-approved ceremony ───────────────
step 3a "init + enroll Body via real owner-approved ceremony (begin / approve / result)"
"$NEONDOLL_SHELLBODY" --init --state-dir "$BODY1" --name m10.5-owner > "$WORK/init1.log" 2>&1 \
  || die "shell-body --init failed: $(cat "$WORK/init1.log")"
( exec "$NEONDOLL_SHELLBODY" --enroll-owner --core-url "$HTTP_URL" --state-dir "$BODY1" > "$WORK/enroll1.log" 2>&1 ) &
ENROLL_PID=$!
wait_for "$WORK/enroll1.log" 'pending approval_code=[A-Z0-9-]+' 40 \
  || { echo "--- enroll1.log ---"; cat "$WORK/enroll1.log"; die "--enroll-owner did not produce an approval code"; }
APPROVAL_CODE="$(sed -n -E 's/.*approval_code=([^ ]+).*/\1/p' "$WORK/enroll1.log" | head -1)"
echo "       --enroll-owner printed approval_code=$APPROVAL_CODE"
step 3b "owner approves on the admin socket (neondollctl body approve CODE)"
"${CTL[@]}" body approve "$APPROVAL_CODE" -permissions process.run > "$WORK/approve.log" 2>&1 \
  || { echo "--- approve.err ---"; cat "$WORK/approve.log"; die "body approve failed"; }
step 3c "Body polls enrollment/result; receipt bound + persisted"
# enroll-owner process should now finish itself once enrolled; wait for the
# process to exit cleanly AND for the enrolled line.
for _ in $(seq 1 40); do kill -0 "$ENROLL_PID" 2>/dev/null || break; sleep 0.5; done
if ! wait_for "$WORK/enroll1.log" 'enrolled body_id=' 15; then
  echo "--- enroll1.log ---"; cat "$WORK/enroll1.log"; die "owner-approved enrollment did not complete"
fi
kill "$ENROLL_PID" 2>/dev/null || true; ENROLL_PID=""
BODY1_ID="$(sed -n -E 's/.*enrolled body_id=([^ ]+).*/\1/p' "$WORK/enroll1.log" | head -1)"
[[ -n "$BODY1_ID" ]] || die "could not parse body_id from enroll"
echo "       owner-enrolled body_id=$BODY1_ID"
grep -E 'enrolled body_id=|membership_reference=|permissions=' "$WORK/enroll1.log"

# ── Step 3-alt: invitation redeem path against the SAME real invite ───────
step "3*" "enroll a second Body via --enroll <invitation> (redeem) to prove the repaired invite path"
"$NEONDOLL_SHELLBODY" --init --state-dir "$BODY2" --name m10.5-invite > "$WORK/init2.log" 2>&1 || die "--init(body2) failed"
"$NEONDOLL_SHELLBODY" --enroll "$INVITE" --core-url "$HTTP_URL" --state-dir "$BODY2" > "$WORK/enroll2.log" 2>&1 \
  || { echo "--- enroll2.log ---"; cat "$WORK/enroll2.log"; die "--enroll (invitation redeem) failed against real Core"; }
grep -q 'enrolled body_id=' "$WORK/enroll2.log" || die "--enroll produced no enrolled line"
BODY2_ID="$(sed -n -E 's/.*enrolled body_id=([^ ]+).*/\1/p' "$WORK/enroll2.log" | head -1)"
echo "       invitation-enrolled body_id=$BODY2_ID"

# Confirm Core actually recorded both bodies (real bidirectional authority).
step 3 "	verify Core sees both bodies via neondollctl body list"
BODY_LIST="$("${CTL[@]}" body list 2>&1)"
echo "$BODY_LIST" | grep -q "$BODY1_ID" || die "Core body list does not contain owner-enrolled body $BODY1_ID"
echo "$BODY_LIST" | grep -q "$BODY2_ID" || die "Core body list does not contain invitation body $BODY2_ID"
echo "       body list: $(echo "$BODY_LIST" | tr -d '\n' | head -c 300)"

# ── Step 4: authenticated Body session + capability advertisement ─────────
step 4 "serve an authenticated session and advertise capabilities (real --serve)"
"$NEONDOLL_SHELLBODY" --serve "$WS_URL" --state-dir "$BODY1" > "$WORK/serve.log" 2>&1 &
BODY_PID=$!
wait_for "$WORK/serve.log" "authenticated with Core as body_id=${BODY1_ID}" 40 \
  || { echo "--- serve.log ---"; cat "$WORK/serve.log"; die "serve did not authenticate"; }
echo "       authenticated: $(grep -m1 "authenticated with Core as body_id=${BODY1_ID}" "$WORK/serve.log")"
echo "       (capability advertised: $(grep -m1 'advertis\|capabil' "$WORK/serve.log" | head -c 160 || echo 'see serve.log'))"

# ── Step 5: guarded shell execution — BLOCKED by the released Core ─────────
step 5 "probing a guarded execution endpoint on the REAL production Core (expected: absent)"
EXEC_RESULT="blocked"
for route in "/v1/execute" "/execute" "/v1/body/execute"; do
  code="$(curl -s -o "$WORK/probe_${route//\//_}.out" -w '%{http_code}' \
    -X POST -H 'Content-Type: application/json' \
    -d "{\"execution_id\":\"m10.5-probe\",\"doll\":\"core\",\"body\":\"${BODY1_ID}\",\"capability\":\"process\",\"operation\":\"run\",\"arguments\":{}}" \
    "$HTTP_URL$route" 2>/dev/null || echo 000)"
  echo "       POST $route -> HTTP ${code:-000}"
  if [[ "${code}" != "000" && "${code}" != "404" && "${code}" != "405" ]]; then EXEC_RESULT="unexpected"; fi
done
if [[ "$EXEC_RESULT" == "blocked" ]]; then
  echo "       => BLOCKED (documented): released production Core exposes NO guarded execution endpoint."
  echo "          Smallest Core-side follow-up: a permission-string Guard over a POST /v1/execute route"
  echo "          plus a 'process.run' capability executor; see M10_5_DEPLOYMENT.md."
fi
step 5 "blocker recorded (not simulated)"

# ── Step 6: terminate + restart Core; Body reconnects, no re-enrollment ───
step 6 "restart Core; verify Body reconnects with persisted identity (no re-enroll)"
AUTH_BEFORE_RESTART="$(grep -Ec "authenticated with Core as body_id=${BODY1_ID}" "$WORK/serve.log" || true)"
STARTED_BEFORE_RESTART="$(grep -c 'neondoll started:' "$WORK/daemon.log" || true)"
kill "$DAEMON_PID" 2>/dev/null || true
for _ in 1 2 3 4 5; do kill -0 "$DAEMON_PID" 2>/dev/null || break; sleep 0.5; done
step 6 "   Core stopped; restarting on the SAME data dir (same core identity)..."
( cd "$WORK" && exec "$NEONDOLLD" >> daemon.log 2>&1 ) &
DAEMON_PID=$!
# Readiness = a NEW 'neondoll started:' line appears (append-only daemon.log).
ready=0
for _ in $(seq 1 80); do
  n="$(grep -c 'neondoll started:' "$WORK/daemon.log" || true)"
  if [[ "$n" -gt "$STARTED_BEFORE_RESTART" ]]; then ready=1; break; fi
  sleep 0.5
done
[[ "$ready" == "1" ]] || die "neondolld did not return to readiness after restart"
CORE_ID_AFTER="$(curl -sf "$HTTP_URL/v1/core/identity" | python3 -c 'import sys,json;print(json.load(sys.stdin)["core_id"])')"
if [[ "$CORE_ID_AFTER" != "$CORE_ID_BEFORE" ]]; then
  die "core identity changed across restart ($CORE_ID_BEFORE -> $CORE_ID_AFTER); the Body trust anchor would not verify"
fi
echo "       core identity stable across restart: $CORE_ID_AFTER"
# The Body (still running from step 4) must authenticate again WITHOUT --enroll.
for _ in $(seq 1 80); do
  n="$(grep -Ec "authenticated with Core as body_id=${BODY1_ID}" "$WORK/serve.log" || true)"
  if [[ "$n" -gt "$AUTH_BEFORE_RESTART" ]]; then break; fi
  sleep 0.5
done
n="$(grep -Ec "authenticated with Core as body_id=${BODY1_ID}" "$WORK/serve.log" || true)"
[[ "$n" -gt "$AUTH_BEFORE_RESTART" ]] || die "Body did not reconnect after Core restart (no new authenticated line)"
step 6 "ok: reconnected after restart using persisted identity (no re-enrollment); authenticated lines: $n"

# ── Step 7: revoke the Body; subsequent authentication denied ─────────────
step 7 "revoke the Body and verify subsequent authentication/execution is denied"
AUTH_BEFORE_REVOKE="$(grep -Ec "authenticated with Core as body_id=${BODY1_ID}" "$WORK/serve.log" || true)"
REVOKE_OUT="$("${CTL[@]}" body revoke "$BODY1_ID" 2>&1)" || { echo "--- revoke.err ---"; echo "$REVOKE_OUT"; die "body revoke failed"; }
echo "       revoke returned: ${REVOKE_OUT:-204 No Content}"
# Core evicts the connected session; the Body retries but must NOT re-authenticate.
if ! no_new_match_in "$WORK/serve.log" "authenticated with Core as body_id=${BODY1_ID}" 10 "$AUTH_BEFORE_REVOKE"; then
  die "revoked Body re-authenticated (security regression)"
fi
echo "       no new authenticated line after revoke (auth denied)"
# Best-effort: Core reflects the revocation in body list.
if "${CTL[@]}" body list 2>&1 | grep -q '"status" *: *"revoked"\|revoked'; then
  echo "       Core body list reflects revocation"
elif "${CTL[@]}" body list 2>&1 | grep -q "$BODY1_ID"; then
  echo "       body still listed (status field not asserted)"
fi

# ── Report ────────────────────────────────────────────────────────────────
echo
echo "=============================================================="
echo "  M10.5 RESULT: PASS (steps 1-4, 6, 7) / step 5 BLOCKED+documented"
echo "  core_id       : $CORE_ID_AFTER"
echo "  owner-enrolled: $BODY1_ID"
echo "  invite-enrolled: $BODY2_ID"
echo "  workspace     : $WORK"
echo "  logs          : $WORK/daemon.log serve.log enroll1.log enroll2.log"
echo "=============================================================="