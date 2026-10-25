#!/usr/bin/env bash
#
# run_m11_1_harness.sh — M11.1 Real Terminal Conversation Proof.
#
# Proves a real neonDoll Shell Body binary opens a real Core 5 Interaction
# Session and carries a terminal conversation over it: a human's typed line is
# sent through the authenticated Doll Link WebSocket as a message event, Core's
# real interaction service runs real cognition against a live (fixture) model
# provider, the Doll's reply is returned to the Shell Body and printed in the
# terminal, and the session closes cleanly on end of input.
#
# The harness deliberately runs the PRODUCTION binaries as separate OS
# processes with isolated temporary state and loopback networking. It never
# mocks Core, never uses in-process fixtures, and never bypasses authentication
# or session authorization. The only fixture is the OpenAI-compatible model
# endpoint (the same class of fixture Core's own E2E suite uses) that Core's
# real inference provider talks to — Core's cognition path itself runs for real.
#
# Usage:
#   NEONDOLLD=/path/neondolld \
#   NEONDOLLCTL=/path/neondollctl \
#   NEONDOLL_SHELLBODY=/path/neondoll-shellbody \
#   ./test/deploy/run_m11_1_harness.sh
#
# Optional env:
#   M11_1_CORE_PORT   loopback HTTP/WS port (default: dynamic pick)
#   M11_1_MODEL_PORT  loopback port for the fixture model (default: CORE_PORT+1)
#   M11_1_WORKDIR     keep a working directory (no cleanup)
#   M11_1_TRACE=1     bash -x trace
#
# Exit 0 = real E2E conversation proven (open → send → reply displayed → close).
set -euo pipefail

if [[ "${M11_1_TRACE:-0}" == "1" ]]; then set -x; fi

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

pick_port() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'
  else
    echo 18080
  fi
}
CORE_PORT="${M11_1_CORE_PORT:-$(pick_port)}"
MODEL_PORT="${M11_1_MODEL_PORT:-$((CORE_PORT+1))}"
WS_URL="ws://127.0.0.1:${CORE_PORT}/ws"
HTTP_URL="http://127.0.0.1:${CORE_PORT}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/m11.1.XXXXXX")"
DATA_DIR="$WORK/data"; STATE_DIR="$WORK/state"; SECRETS_DIR="$WORK/secrets"; BODY="$WORK/body"
if [[ -n "${M11_1_WORKDIR:-}" ]]; then WORK="$M11_1_WORKDIR"; WORK_PINNED=1; mkdir -p "$WORK"; fi
mkdir -p "$DATA_DIR" "$STATE_DIR" "$SECRETS_DIR" "$BODY"

DAEMON_PID=""; STUB_PID=""
cleanup() {
  local rc=$?
  trap - EXIT INT TERM
  [[ -n "$STUB_PID"  ]] && kill "$STUB_PID"  2>/dev/null || true
  [[ -n "$DAEMON_PID" ]] && kill "$DAEMON_PID" 2>/dev/null || true
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
step() { printf '%-24s %s\n' "[step $1]" "$2"; }
wait_for() {
  local log="$1" pat="$2" budget="${3:-40}" i=0
  while [[ $i -lt $((budget*2)) ]]; do
    if [[ -f "$log" ]] && grep -Eq "$pat" "$log" 2>/dev/null; then return 0; fi
    sleep 0.5; i=$((i+1))
  done
  return 1
}

# Fixture OpenAI-compatible model provider (real HTTP server behind Core's own
# inference provider; echoes the last user message so every reply visibly
# round-trips through Core's cognition).
cat > "$WORK/stub.py" <<EOF
from http.server import BaseHTTPRequestHandler, HTTPServer
import json, sys
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(n) if n else b"{}"
        try: req = json.loads(body)
        except Exception: req = {}
        texts = [str(m.get("content","")) for m in (req.get("messages") or []) if m.get("role")=="user"]
        reply = "re: " + (texts[-1] if texts else "-") + " — hello from the Core doll"
        data = {"choices":[{"message":{"role":"assistant","content":reply}}]}
        payload = json.dumps(data).encode()
        self.send_response(200); self.send_header("Content-Type","application/json")
        self.send_header("Content-Length", str(len(payload))); self.end_headers()
        self.wfile.write(payload)
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
EOF
python3 "$WORK/stub.py" "$MODEL_PORT" > "$WORK/stub.log" 2>&1 &
STUB_PID=$!
[[ -n "$STUB_PID" ]] && kill -0 "$STUB_PID" 2>/dev/null || die "fixture model did not start"

say "M11.1 Real Terminal Conversation harness"
echo "  core        : $NEONDOLLD"
echo "  shellbody   : $NEONDOLL_SHELLBODY"
echo "  ws_url      : $WS_URL"
echo "  model       : http://127.0.0.1:${MODEL_PORT} (fixture)"
echo "  work        : $WORK"

# ── Step 1: real Core 5 daemon (pulse on so the host doll is created) ─────
step 1 "launch real Core daemon (Core 5 interaction session)"
cat > "$WORK/config.json" <<EOF
{
  "core": {"profile": "default", "environment": "development", "pulse": {"enabled": true}},
  "console": {"enabled": false, "prompt": "> "},
  "inference": {
    "default_model": "default",
    "models": {"default": {"provider": "openai", "model": "default", "endpoint": "http://127.0.0.1:${MODEL_PORT}", "budget": {"context_tokens": 4096, "output_tokens": 1024, "required_overhead_tokens": 256}}},
    "tasks": {"interaction": "default", "orient": "default", "plan": "default", "direct_cognition": "default"}
  },
  "paths": {"state_dir": "$STATE_DIR", "secrets_dir": "$SECRETS_DIR", "data_dir": "$DATA_DIR"},
  "link": {"websocket": {"enabled": true, "listen": "127.0.0.1:${CORE_PORT}"}},
  "http": {"enabled": true, "listen": "127.0.0.1:${CORE_PORT}", "enable_cors": true},
  "relay": {"enabled": false, "url": "", "token": ""}
}
EOF
( cd "$WORK" && exec "$NEONDOLLD" > daemon.log 2>&1 ) &
DAEMON_PID=$!
wait_for "$WORK/daemon.log" 'neondoll started:' 40 || {
  echo "--- daemon.log ---"; tail -40 "$WORK/daemon.log" || true; die "neondolld did not reach readiness"
}
step 1 "ok: daemon ready; expecting host doll to be created on pulse start"
wait_for "$WORK/daemon.log" 'initial doll state created' 20 \
  || die "Core did not create the host doll (is pulse enabled and is the model provider reachable?)"
grep -m1 'initial doll state created' "$WORK/daemon.log" | sed 's/^/       /'
DOLL_ID="$(sed -n -E 's/.*"doll_id":"([^"]+)".*/\1/p' "$WORK/daemon.log" | tail -1)"
DOLL_ID="${DOLL_ID:-default}"
echo "       addressing doll_id=$DOLL_ID"
CORE_ID="$(curl -sf "$HTTP_URL/v1/core/identity" | python3 -c 'import sys,json;print(json.load(sys.stdin)["core_id"])' 2>/dev/null || echo unknown)"
echo "       core_id        : $CORE_ID"

# ── Step 2: owner invites, Shell Body enrolls (real ceremony, real invite) ─
step 2 "enroll the real Shell Body via the real invitation-redeem ceremony"
ADMIN_SOCK="$DATA_DIR/neondoll-admin.sock"
"$NEONDOLLCTL" -socket "$ADMIN_SOCK" body invite -expires 30m -permissions '' -endpoint "$HTTP_URL" > "$WORK/invite.json" 2>"$WORK/invite.err" \
  || die "neondollctl body invite failed: $(cat "$WORK/invite.err")"
grep -q '"invitation_id"' "$WORK/invite.json" || die "invite output missing invitation_id"
"$NEONDOLL_SHELLBODY" --init --state-dir "$BODY" --name m11.1 > "$WORK/init.log" 2>&1 || die "--init failed"
"$NEONDOLL_SHELLBODY" --enroll "$WORK/invite.json" --core-url "$HTTP_URL" --state-dir "$BODY" > "$WORK/enroll.log" 2>&1 || die "--enroll failed"
grep -q 'enrolled body_id=' "$WORK/enroll.log" || die "no enrolled line"
BODY_ID="$(sed -n -E 's/.*enrolled body_id=([^ ]+).*/\1/p' "$WORK/enroll.log" | head -1)"
echo "       enrolled body_id=$BODY_ID"

# ── Step 3: real terminal conversation (open → send → reply → close) ─────
step 3 "run --converse: type two lines, receive the Doll's replies, close"
CONVO_PID=""
( printf 'hello neon doll\nwhat is up?\n' | "$NEONDOLL_SHELLBODY" --converse "$WS_URL" --state-dir "$BODY" --doll "$DOLL_ID" > "$WORK/converse.log" 2>&1 ) &
CONVO_PID=$!
wait_for "$WORK/converse.log" 'authenticated with Core as' 20 2>/dev/null || true
# readiness-ish: the banner line
wait_for "$WORK/converse.log" 'connected to Core' 25 \
  || { echo "--- converse.log ---"; cat "$WORK/converse.log"; die "pending"; }
wait "$CONVO_PID"; CONVO_RC=$?
if [[ "$CONVO_RC" != "0" ]]; then
  echo "--- converse.log ---"; cat "$WORK/converse.log"; die "converse exited $CONVO_RC"
fi
echo "       converse exit: $CONVO_RC"
grep -E 'connected to Core|^re:|conversation ended' "$WORK/converse.log" | sed 's/^/       /'

# ── Step 4: assert the round-trip + clean close + Core persisted the turn ──
step 4 "assert the real reply appeared and Core persisted the interaction turn"
REPLY_COUNT="$(grep -Ec '^re: ' "$WORK/converse.log" || true)"
if [[ "$REPLY_COUNT" -lt 2 ]]; then
  echo "--- converse.log ---"; cat "$WORK/converse.log"; die "expected at least 2 Doll replies, got $REPLY_COUNT"
fi
grep -q '^re: hello neon doll — hello from the Core doll' "$WORK/converse.log" || die "first reply did not round-trip the typed line"
grep -q '^re: what is up? — hello from the Core doll'    "$WORK/converse.log" || die "second reply did not round-trip the typed line"
grep -q 'conversation ended (eof' "$WORK/converse.log" || die "conversation did not end cleanly on EOF"
wait_for "$WORK/daemon.log" 'interaction served and persisted' 15 \
  || die "Core did not log 'interaction served and persisted' for a turn"
PERSISTED="$(grep -c 'interaction served and persisted' "$WORK/daemon.log" || true)"
if [[ "$PERSISTED" -lt 2 ]]; then die "expected >=2 persisted turns, got $PERSISTED"; fi
grep -m1 'interaction served and persisted' "$WORK/daemon.log" | sed 's/^/       /'

echo -e "\n== M11.1 RESULT: PASS — real terminal conversation (open → send → reply → close) =="
echo "   body_id             : $BODY_ID"
echo "   doll_id             : $DOLL_ID"
echo "   core (sha)          : detected from daemon identity via /v1/core/identity"
echo "   replies displayed   : $REPLY_COUNT"
echo "   turns persisted     : $PERSISTED"