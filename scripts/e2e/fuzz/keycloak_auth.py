"""Proactively refresh the short-lived Keycloak token during long fuzz runs.

Schemathesis caches the token for four minutes and refreshes it immediately on
401 as well. fetch_token.py supplies the direct grant using only stdlib.
"""

import schemathesis

from fetch_token import fetch_token


@schemathesis.auth(refresh_interval=240, retry_on=[401])
class KeycloakAuth:
    def get(self, case, ctx) -> str:
        return fetch_token()

    def set(self, case, data: str, ctx) -> None:
        case.headers = case.headers or {}
        case.headers["Authorization"] = f"Bearer {data}"
