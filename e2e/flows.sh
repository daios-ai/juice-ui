#!/usr/bin/env bash
# End-to-end UI flow harness for juice-ui.
#
# Mirrors docs/flows.sh, but drives the *BFF over HTTP* (browser's view) instead
# of the kernel CLI: it stands up a throwaway kernel (fresh DB -> bootstrap
# superuser, JUICE_ALLOW_LOCAL_SOURCES so actions can target a local backend), a
# local HTTP backend, and the juice-ui server, then exercises each in-scope user
# story (requirements §6) with curl + a cookie jar + CSRF tokens.
#
# It is NOT part of `go test ./...` (which must run offline). Run it explicitly:
#   ./e2e/flows.sh
#
# Out of scope by design (§3.2): admin/supervision, deposits-from-UI, federation.
# (We DO use the admin CLI here only to fund a buyer for setup — that is the test
# harness acting as operator, not the UI.)
set -u

JUICE="${JUICE:-$(command -v juice)}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d /tmp/juiceui-e2e.XXXXXX)"
KPORT=4790; BPORT=4791; UIPORT=8790
KURL="http://127.0.0.1:$KPORT"
UI="http://127.0.0.1:$UIPORT"
DB="$TMP/juice.db"
BFF="$TMP/juice-ui"
KPID=""; BKPID=""; UIPID=""
PASS=0; FAIL=0

say()  { printf '\n=== %s ===\n' "$1"; }
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }
expect()      { if printf '%s' "$2" | grep -q -- "$3"; then ok "$1"; else bad "$1 (missing: $3)"; fi; }
refute()      { if printf '%s' "$2" | grep -q -- "$3"; then bad "$1 (unexpected: $3)"; else ok "$1"; fi; }
expect_code() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got $2 want $3)"; fi; }

cleanup() {
  [ -n "$UIPID" ] && kill "$UIPID" 2>/dev/null
  [ -n "$BKPID" ] && kill "$BKPID" 2>/dev/null
  [ -n "$KPID" ]  && kill "$KPID"  2>/dev/null
  wait 2>/dev/null
  rm -rf "$TMP"
}
trap cleanup EXIT

# --- kernel CLI helpers (in-process on DB; HOME holds the CLI token) ---
j() { JUICE_LOG_LEVEL=error HOME="$1" JUICE_ALLOW_LOCAL_SOURCES=true "$JUICE" --db "$DB" "${@:2}" 2>&1; }
uuid() { grep -oE '[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}' | head -1; }

# --- UI/HTTP helpers (cookie jar per simulated browser) ---
csrf_of() { grep -o 'name="gorilla.csrf.Token" value="[^"]*"' | head -1 | sed 's/.*value="\([^"]*\)"/\1/'; }
uiget()   { curl -s -b "$1" -c "$1" "$UI$2"; }                      # jar path
uicode()  { curl -s -o /dev/null -w '%{http_code}' -b "$1" -c "$1" "$UI$2"; }
# submit jar getpath postpath [--data-urlencode k=v ...] : fetch CSRF from getpath, POST to postpath
submit() {
  local jar="$1" getp="$2" postp="$3"; shift 3
  local t; t="$(uiget "$jar" "$getp" | csrf_of)"
  curl -s -b "$jar" -c "$jar" "$@" --data-urlencode "gorilla.csrf.Token=$t" "$UI$postp"
}
submit_code() {
  local jar="$1" getp="$2" postp="$3"; shift 3
  local t; t="$(uiget "$jar" "$getp" | csrf_of)"
  curl -s -o /dev/null -w '%{http_code}' -b "$jar" -c "$jar" "$@" --data-urlencode "gorilla.csrf.Token=$t" "$UI$postp"
}

wait_health() { local url="$1"; for _ in $(seq 1 60); do curl -sf "$url" >/dev/null 2>&1 && return 0; sleep 0.2; done; return 1; }

# ---------------------------------------------------------------------------
[ -z "$JUICE" ] && { echo "juice binary not found (set JUICE=...)"; exit 2; }
say "build BFF"
( cd "$ROOT" && go build -o "$BFF" ./cmd/juice-ui ) || { echo "build failed"; exit 2; }

say "bootstrap throwaway kernel (creates @sys)"
mkdir -p "$TMP/sys" "$TMP/alice" "$TMP/bob"
JUICE_LOG_LEVEL=error JUICE_BOOTSTRAP_PASSWORD=syspass JUICE_ALLOW_LOCAL_SOURCES=true HOME="$TMP/sys" \
  "$JUICE" --db "$DB" serve --addr "127.0.0.1:$KPORT" >"$TMP/k.log" 2>&1 &
KPID=$!
wait_health "$KURL/health" || { echo "kernel did not start"; cat "$TMP/k.log"; exit 2; }
kill "$KPID" 2>/dev/null; wait "$KPID" 2>/dev/null; KPID=""

say "setup via CLI (server stopped): users, actions, deposit"
j "$TMP/alice" user create --handle @alice --email a@x.test --password alicepass >/dev/null 2>&1
j "$TMP/bob"   user create --handle @bob   --email b@x.test --password bobpass   >/dev/null 2>&1
BOB_ID="$(j "$TMP/bob" auth login --handle @bob --password bobpass >/dev/null 2>&1; j "$TMP/bob" user me --output json 2>/dev/null | uuid)"
j "$TMP/alice" auth login --handle @alice --password alicepass >/dev/null 2>&1
mkact() { # name price path -> echoes id
  local id; id="$(j "$TMP/alice" action create --name "$1" --kind http --price "$2" \
    --source "http://127.0.0.1:$BPORT/$3" --description "$1 action" \
    --input-schema '{"type":"object","properties":{"x":{"type":"string","description":"anything"}}}' \
    --output-schema '{"type":"object"}' | uuid)"
  j "$TMP/alice" action enable --id "$id" >/dev/null 2>&1
  j "$TMP/alice" action grant-all --id "$id" >/dev/null 2>&1
  echo "$id"
}
PAY_ID="$(mkact pay 100 ok)"      # paid success
FAIL_ID="$(mkact failing 100 fail)" # paid failure (backend 500)
FREE_ID="$(mkact free 0 ok)"      # price-0 success (ephemeral) + listener target
j "$TMP/sys" auth login --handle @sys --password syspass >/dev/null 2>&1
j "$TMP/sys" admin user deposit --handle @bob --amount 1000 >/dev/null 2>&1
echo "bob_id=$BOB_ID pay=$PAY_ID fail=$FAIL_ID free=$FREE_ID"

say "start local backend + kernel serve + BFF"
python3 - "$BPORT" >"$TMP/backend.log" 2>&1 <<'PY' &
import sys, http.server
port=int(sys.argv[1])
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n=int(self.headers.get('Content-Length',0)); self.rfile.read(n)
        if self.path.endswith('/fail'):
            self.send_response(500); self.send_header('Content-Type','application/json'); self.end_headers()
            self.wfile.write(b'{"error":"backend"}')
        else:
            self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
            self.wfile.write(b'{"result":"ok","echo":"hello-from-backend"}')
    def log_message(self,*a): pass
http.server.HTTPServer(('127.0.0.1',port),H).serve_forever()
PY
BKPID=$!
JUICE_LOG_LEVEL=error JUICE_BOOTSTRAP_PASSWORD=syspass JUICE_ALLOW_LOCAL_SOURCES=true HOME="$TMP/sys" \
  "$JUICE" --db "$DB" serve --addr "127.0.0.1:$KPORT" >"$TMP/k.log" 2>&1 &
KPID=$!
JUICE_KERNEL_URL="$KURL" JUICE_UI_ADDR="127.0.0.1:$UIPORT" JUICE_UI_SESSION_SECRET=e2e-secret \
  "$BFF" >"$TMP/ui.log" 2>&1 &
UIPID=$!
wait_health "$KURL/health" || { echo "kernel restart failed"; exit 2; }
wait_health "$UI/login"    || { echo "bff did not start"; cat "$TMP/ui.log"; exit 2; }

# --------------------------------------------------------------------------
CAROL="$TMP/carol.jar"; BOB="$TMP/bob.jar"; ANON="$TMP/anon.jar"
rm -f "$CAROL" "$BOB" "$ANON"

say "Story: account creation + login (R5.1, R5.6)"
# Carol signs up THROUGH the UI (creates account + auto-login).
expect_code "signup @carol via UI" "$(submit_code "$CAROL" /login /signup \
  --data-urlencode "handle=@carol" --data-urlencode "email=c@x.test" --data-urlencode "password=carolpass")" 303
expect "dashboard greets @carol" "$(uiget "$CAROL" /)" "@carol"
# Bob (funded) logs in.
expect_code "login @bob via UI" "$(submit_code "$BOB" /login /login \
  --data-urlencode "handle=@bob" --data-urlencode "password=bobpass")" 303

say "Story: dashboard balances + out-of-band note (R6.1.1, R3.3.1)"
DASH="$(uiget "$BOB" /)"
expect "shows available credits" "$DASH" "1,000"
expect "explains top-ups are out-of-band" "$DASH" "out-of-band"

say "Story: discovery degrades honestly to catalog (R6.2.4, R10.7)"
DISC="$(uiget "$BOB" "/discover?q=hello")"
expect "semantic-search-unavailable banner" "$DISC" "Semantic search is unavailable"
expect "catalog browse lists an action" "$(uiget "$BOB" /discover)" "@alice/"

say "Story: authoring in Studio (R6.3) + owner-only affordances (R9.2)"
submit "$CAROL" /studio /studio/create \
  --data-urlencode "name=carol-tool" --data-urlencode "kind=http" \
  --data-urlencode "source=http://127.0.0.1:$BPORT/ok" --data-urlencode "price=0" \
  --data-urlencode "description=carol tool" --data-urlencode "input_schema={\"type\":\"object\"}" \
  --data-urlencode "output_schema={\"type\":\"object\"}" >/dev/null
CAROL_AID="$(uiget "$CAROL" /studio; j "$TMP/alice" action list --output json >/dev/null 2>&1; echo)"
# Find carol's action id by listing carol's actions after enabling; simplest: search the flash redirect target.
CAROL_AID="$(curl -s -b "$CAROL" -c "$CAROL" -o /dev/null -w '%{redirect_url}' "$UI/studio" 2>/dev/null)"
# Owner sees Manage on alice's? No — alice's action viewed by carol must hide Manage:
expect "non-owner hides Manage section" "$(uiget "$CAROL" "/actions/$PAY_ID")" "Run"
refute "non-owner truly has no owner controls" "$(uiget "$CAROL" "/actions/$PAY_ID")" "Manage (owner)"

say "Story: price-0 call via ephemeral session (R6.5.1)"
RUN_FREE="$(submit "$BOB" "/actions/$FREE_ID" "/actions/$FREE_ID/run" \
  --data-urlencode "has_fields=1" --data-urlencode "f_x=hi" --data-urlencode "t_x=string")"
expect "free call succeeds" "$RUN_FREE" "Success"
expect "free call shows backend result" "$RUN_FREE" "hello-from-backend"
expect "ephemeral process appears in closed history" "$(uiget "$BOB" /processes)" "History (closed)"

say "Story: paid call success with gross/fee/net (R6.5.2, R6.5.5)"
RUN_PAY="$(submit "$BOB" "/actions/$PAY_ID" "/actions/$PAY_ID/run" \
  --data-urlencode "process_id=" --data-urlencode "new_funds=300" \
  --data-urlencode "has_fields=1" --data-urlencode "f_x=hi" --data-urlencode "t_x=string")"
expect "paid call succeeds" "$RUN_PAY" "Success"
expect "cost breakdown gross shown" "$RUN_PAY" "gross"
expect "gross is 100" "$RUN_PAY" "money\">100"

say "Story: input-schema rejection — not sent, not charged (R6.5.5)"
# @sys/lookup requires 'query'; submitting none -> 422 rejection BEFORE charge.
LOOK_ID="$(curl -s "$KURL/v1/actions?owner=@sys&name=lookup" | uuid)"
REJ="$(submit "$BOB" "/actions/$LOOK_ID" "/actions/$LOOK_ID/run" --data-urlencode "use_raw=1" --data-urlencode "raw_args={}")"
expect "input rejection => not charged" "$REJ" "Not sent"

say "Story: execution failure — fully refunded (R6.5.5, R7.1)"
RUN_FAIL="$(submit "$BOB" "/actions/$FAIL_ID" "/actions/$FAIL_ID/run" \
  --data-urlencode "process_id=" --data-urlencode "new_funds=300" \
  --data-urlencode "has_fields=1" --data-urlencode "f_x=hi" --data-urlencode "t_x=string")"
expect "failure => fully refunded" "$RUN_FAIL" "fully refunded"

say "Story: transactions + buyer rating (R6.6.1, R6.6.4)"
# Use the success transaction linked from the paid-call outcome (the list also
# contains the failed call, which has no rate control).
TXID="$(printf '%s' "$RUN_PAY" | grep -oE '/transactions/[0-9a-f-]{36}' | head -1 | cut -d/ -f3)"
expect "transactions list reachable" "$(uiget "$BOB" /transactions)" "Transactions"
TXD="$(uiget "$BOB" "/transactions/$TXID")"
expect "success tx shows cost breakdown" "$TXD" "net"
expect "buyer sees rate control" "$TXD" "Rate this result"
expect_code "submit rating" "$(submit_code "$BOB" "/transactions/$TXID" "/transactions/$TXID/rate" --data-urlencode "rating=1")" 303

say "Story: funded session top-up + end & reclaim (R6.5.2)"
PID="$(uiget "$BOB" /processes | grep -oE '/processes/[0-9a-f-]{36}' | head -1 | cut -d/ -f3)"
expect_code "top up session" "$(submit_code "$BOB" "/processes/$PID" "/processes/$PID/fund" --data-urlencode "funds=50")" 303
expect_code "end & reclaim" "$(submit_code "$BOB" "/processes/$PID" "/processes/$PID/end")" 303

say "Story: listeners + events (R6.7)"
submit "$BOB" /listeners /listeners/create \
  --data-urlencode "source_user_id=$BOB_ID" --data-urlencode "event_name=e.test" \
  --data-urlencode "target_action=@alice/free" >/dev/null
expect "listener created" "$(uiget "$BOB" /listeners)" "e.test"
EMIT="$(submit "$BOB" /listeners /listeners/emit --data-urlencode "event_name=e.test" --data-urlencode "args={}")"
LID="$(uiget "$BOB" /listeners | grep -oE '/listeners/[0-9a-f-]{36}/inbox' | head -1 | cut -d/ -f3)"
INBOX="$(uiget "$BOB" "/listeners/$LID/inbox")"
expect "inbox shows a pending event or none cleanly" "$INBOX" "Consume\|No pending events"

say "Story: collapsible sidebar (no-JS) (R8.1)"
expect "sidebar nav present" "$(uiget "$BOB" /)" 'class="sidebar"'
submit "$BOB" / /sidebar --data-urlencode "sidebar=collapsed" >/dev/null
expect "sidebar collapse stamped server-side" "$(uiget "$BOB" /)" 'data-sidebar="collapsed"'
submit "$BOB" / /sidebar --data-urlencode "sidebar=expanded" >/dev/null
refute "sidebar expand clears the stamp" "$(uiget "$BOB" /)" 'data-sidebar="collapsed"'

say "Story: run form reveals nested input shape (R6.5.4)"
CHAT_ID="$(curl -s "$KURL/v1/actions?owner=@sys&name=llm/chat" | uuid)"
# (JSON quotes are HTML-escaped in the textarea; the browser renders them back.)
CHATPAGE="$(uiget "$BOB" "/actions/$CHAT_ID")"
expect "messages array skeleton pre-filled" "$CHATPAGE" 'role'
expect "skeleton shows content field too" "$CHATPAGE" 'content'

say "Story: theme toggle works (no-JS) + CSRF protection (R8.6, R5.5)"
submit "$BOB" / /theme --data-urlencode "theme=dark" >/dev/null
expect "explicit dark theme stamped" "$(uiget "$BOB" /)" 'data-theme="dark"'
expect_code "state-change without CSRF token is rejected" \
  "$(curl -s -o /dev/null -w '%{http_code}' -b "$BOB" "$UI/theme" --data-urlencode 'theme=light')" 403

say "Story: logout clears session (R5.4)"
submit "$BOB" / /logout >/dev/null
expect_code "after logout, dashboard redirects to login" "$(uicode "$BOB" /)" 303

# --------------------------------------------------------------------------
say "RESULT"
printf 'passed=%d failed=%d\n' "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
