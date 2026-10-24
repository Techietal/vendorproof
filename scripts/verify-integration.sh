#!/usr/bin/env bash
# Proves the PostgreSQL integration suites actually ran. Any skip is a failure.
set -euo pipefail
cd "$(dirname "$0")/.."
export REQUIRE_DATABASE=1

packages=(./internal/database ./internal/organizations ./internal/httpserver)
log="$(mktemp)"
trap 'rm -f "$log"' EXIT

if ! go test -json -count=1 "${packages[@]}" >"$log"; then
  echo "integration tests failed:" >&2
  grep -v '"Action":"output"' "$log" | tail -60 >&2
  exit 1
fi

python3 - "$log" "${packages[@]}" <<'PY'
import collections, json, sys

expected = {p.removeprefix("./") for p in sys.argv[2:]}
passed = collections.Counter()
skipped, failed = [], []
for line in open(sys.argv[1]):
    try:
        event = json.loads(line)
    except ValueError:
        continue
    action, test = event.get("Action"), event.get("Test")
    pkg = event.get("Package", "").removeprefix("vendorproof/")
    if action == "pass" and test:
        passed[pkg] += 1
    elif action == "skip" and test:
        skipped.append(f"{pkg}.{test}")
    elif action == "fail" and test:
        failed.append(f"{pkg}.{test}")

if failed:
    sys.exit("failed:\n  " + "\n  ".join(failed))
if skipped:
    sys.exit("skipped despite REQUIRE_DATABASE=1:\n  " + "\n  ".join(skipped))
for pkg in sorted(expected):
    if passed[pkg] == 0:
        sys.exit(f"{pkg}: no tests executed")
    print(f"{pkg}: {passed[pkg]} test/subtest passes")
print("no PostgreSQL-dependent test was skipped")
PY
