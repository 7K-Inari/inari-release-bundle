#!/bin/sh
# Run the pinned Schemathesis config and always preserve its reports, including
# the check-failure path. The caller supplies the pulled OCI spec as $1.
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SCHEMA=${1:-/spec/dist/openapi.yaml}

: "${E2E_BASE_DOMAIN:?E2E_BASE_DOMAIN is required}"
: "${E2E_FUZZ_ORG:?E2E_FUZZ_ORG is required}"
if [ ! -s "$SCHEMA" ]; then
  echo "fuzz: OpenAPI schema not found or empty at $SCHEMA (spec pull/publish gap)" >&2
  exit 2
fi

mkdir -p /artifacts/out /artifacts/report
cp "$SCHEMA" /artifacts/report/openapi.yaml
cp "$SCRIPT_DIR/schemathesis.toml" /artifacts/report/schemathesis.toml

# Only the token is captured; fetch_token.py never logs credentials or KC error
# bodies. Schemathesis sanitizes the Authorization header in its own reports.
E2E_TOKEN=$(python3 "$SCRIPT_DIR/fetch_token.py")
export E2E_TOKEN
python3 "$SCRIPT_DIR/ensure_tenant.py"

set +e
st --config-file "$SCRIPT_DIR/schemathesis.toml" run "$SCHEMA"
STATUS=$?
set -e

if [ -s /artifacts/out/junit.xml ]; then
  # Keep the ReportPortal suite name stable across Schemathesis releases.
  sed -i 's/<testsuite name="[^"]*"/<testsuite name="fuzz"/' /artifacts/out/junit.xml
else
  echo "fuzz: Schemathesis did not write /artifacts/out/junit.xml (exit $STATUS)" >&2
  # A zero exit without the required result artifact is still a failed leg.
  if [ "$STATUS" -eq 0 ]; then
    STATUS=1
  fi
fi

# Preserve the actual Schemathesis verdict. Argo collects both outputs on
# failure; the exit-handler adds the optional JUnit to the same RP launch.
exit "$STATUS"
