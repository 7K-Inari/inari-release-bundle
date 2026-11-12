"""Fetch the dev-admin direct-grant token using only Python's stdlib.

Credentials stay in the POST body and only the access token is printed to
stdout for run.sh to capture. HTTP failures never include the response body or
credentials in the exception text.
"""

import json
import os
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import Request, urlopen


def _required(name: str) -> str:
    value = os.environ.get(name, "")
    if not value:
        raise RuntimeError(f"{name} is required")
    return value


def fetch_token() -> str:
    kc_url = _required("E2E_KC_URL").rstrip("/")
    realm = os.environ.get("E2E_KC_REALM", "inari")
    request = Request(
        f"{kc_url}/realms/{realm}/protocol/openid-connect/token",
        data=urlencode(
            {
                "grant_type": "password",
                "client_id": "inari-server",
                "username": _required("E2E_USER"),
                "password": _required("E2E_PASSWORD"),
                "scope": "openid organization:*",
            }
        ).encode(),
        headers={"content-type": "application/x-www-form-urlencoded"},
        method="POST",
    )
    try:
        with urlopen(request, timeout=15) as response:
            payload = json.load(response)
    except HTTPError as error:
        raise RuntimeError(f"Keycloak token request failed: HTTP {error.code}") from None
    except URLError as error:
        raise RuntimeError(f"Keycloak token request failed: {error.reason}") from None

    token = payload.get("access_token")
    if not isinstance(token, str) or not token:
        raise RuntimeError("Keycloak token response missing access_token")
    return token


if __name__ == "__main__":
    print(fetch_token())
