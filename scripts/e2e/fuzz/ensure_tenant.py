"""Idempotently ensure the run-owned fuzz tenant exists (stdlib only)."""

import json
import os
from urllib.error import HTTPError, URLError
from urllib.parse import quote
from urllib.request import Request, urlopen


def _required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def _send(request: Request) -> int:
    """Return an HTTP status without logging response bodies or credentials."""
    try:
        with urlopen(request, timeout=15) as response:
            # Consume and close the response; setup needs only the status.
            response.read()
            return response.status
    except HTTPError as error:
        error.close()
        return error.code
    except URLError as error:
        raise RuntimeError(f"tenant setup request failed: {error.reason}") from None


def ensure_tenant(token: str) -> str:
    slug = _required("E2E_FUZZ_ORG")
    base_url = f"https://{_required('E2E_BASE_DOMAIN')}/api/v1/tenants"
    auth = {"Authorization": f"Bearer {token}"}
    create = Request(
        base_url,
        data=json.dumps({"slug": slug, "displayName": f"E2E API fuzz {slug}"}).encode(),
        headers={**auth, "content-type": "application/json"},
        method="POST",
    )

    # A missing org may return 403 rather than 404, so a lookup-first flow
    # cannot distinguish "not created yet" from a genuine authorization bug.
    # Create first; only a known duplicate response justifies a confirmation
    # lookup (500 is the pre-inari-server-#216 duplicate-create behavior).
    create_status = _send(create)
    if create_status == 200:
        return "created"
    if create_status not in (409, 500):
        raise RuntimeError(f"tenant create failed: HTTP {create_status}")

    lookup = Request(f"{base_url}/{quote(slug, safe='')}", headers=auth, method="GET")
    lookup_status = _send(lookup)
    if lookup_status == 200:
        return "existing"
    raise RuntimeError(
        f"tenant create failed: HTTP {create_status}; confirmation lookup returned HTTP {lookup_status}"
    )


if __name__ == "__main__":
    outcome = ensure_tenant(_required("E2E_TOKEN"))
    print(f"fuzz: tenant {os.environ['E2E_FUZZ_ORG']} {outcome}")
