#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/multica-check.XXXXXX")
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/scripts" "$TEST_DIR/server" "$TEST_DIR/bin"
cp "$SCRIPT_DIR/check.sh" "$TEST_DIR/scripts/check.sh"
export CHECK_TEST_DIR="$TEST_DIR"

cat >"$TEST_DIR/.env" <<'ENV'
PORT=18080
FRONTEND_PORT=13000
ENV

cat >"$TEST_DIR/scripts/local-env.sh" <<'FAKE'
# The pipeline no longer starts a server. This stub only has to be sourceable.
:
FAKE
cat >"$TEST_DIR/scripts/ensure-postgres.sh" <<'FAKE'
exit "${PREREQUISITE_STATUS:-0}"
FAKE
printf 'exit 0\n' >"$TEST_DIR/scripts/test-go.test.sh"
printf 'exit 0\n' >"$TEST_DIR/scripts/test-go.sh"
cat >"$TEST_DIR/bin/pnpm" <<'FAKE'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"$CHECK_TEST_DIR/pnpm-calls"
if [ "$*" = "${FAIL_PHASE:-}" ]; then
  exit 23
fi
FAKE
cat >"$TEST_DIR/bin/go" <<'FAKE'
#!/usr/bin/env bash
exit 0
FAKE
chmod 755 "$TEST_DIR/bin/"*

fail() {
  echo "$label: $*" >&2
  cat "$TEST_DIR/output" >&2
  exit 1
}

# $1: label; $2: status; rest: env overrides.
run_case() {
  label=$1 expected_status=$2
  shift 2
  : >"$TEST_DIR/pnpm-calls"
  set +e
  (
    cd "$TEST_DIR"
    env -u BASH_ENV ENV_FILE=.env PATH="$TEST_DIR/bin:$PATH" \
      PREREQUISITE_STATUS=0 FAIL_PHASE= \
      "$@" bash scripts/check.sh
  ) >"$TEST_DIR/output" 2>&1
  status=$?
  set -e
  [ "$status" -eq "$expected_status" ] || fail "status $status, want $expected_status"
  if [ "$expected_status" -eq 0 ]; then
    grep -q '^✓ All checks passed\.$' "$TEST_DIR/output" || fail "missing success footer"
    ! grep -q 'Checks FAILED' "$TEST_DIR/output" || fail "unexpected failure footer"
  else
    grep -q '^✗ Checks FAILED\.$' "$TEST_DIR/output" || fail "missing failure footer"
    ! grep -q 'All checks passed' "$TEST_DIR/output" || fail "misleading success footer"
  fi
  echo "$label: PASS"
}

run_case 'prerequisite failure' 37 PREREQUISITE_STATUS=37
[ ! -s "$TEST_DIR/pnpm-calls" ] || fail 'continued after prerequisite failure'
run_case 'guarded typecheck failure' 1 FAIL_PHASE=typecheck
run_case 'guarded unit test failure' 1 FAIL_PHASE=test
grep -q '^typecheck$' "$TEST_DIR/pnpm-calls" || fail 'unit-test failure skipped typecheck'
grep -q '^test$' "$TEST_DIR/pnpm-calls" || fail 'unit-test failure did not reach the suite'
run_case 'success' 0
grep -q '^typecheck$' "$TEST_DIR/pnpm-calls" || fail 'did not typecheck'
grep -q '^test$' "$TEST_DIR/pnpm-calls" || fail 'did not run unit tests'
! grep -q playwright "$TEST_DIR/pnpm-calls" || fail 'still ran end-to-end'

echo 'check.test.sh: PASS'
