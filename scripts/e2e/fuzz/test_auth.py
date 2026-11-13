"""Contract for proactive Keycloak token refresh during long fuzz runs."""

from types import ModuleType, SimpleNamespace
from unittest.mock import patch
import sys
import unittest


class KeycloakAuthContract(unittest.TestCase):
    def test_refresh_registration_and_authorization_header(self) -> None:
        schemathesis = ModuleType("schemathesis")
        registration = {}

        def auth(**kwargs):
            registration.update(kwargs)
            return lambda provider: provider

        schemathesis.auth = auth
        fetch_token = ModuleType("fetch_token")
        fetch_token.fetch_token = lambda: "fresh-token"
        with patch.dict(
            sys.modules,
            {"schemathesis": schemathesis, "fetch_token": fetch_token},
        ):
            from scripts.e2e.fuzz import keycloak_auth

        self.assertEqual(registration["refresh_interval"], 240)
        self.assertEqual(registration["retry_on"], [401])

        provider = keycloak_auth.KeycloakAuth()
        self.assertEqual(provider.get(None, None), "fresh-token")
        case = SimpleNamespace(headers={"X-Existing": "preserved"})
        provider.set(case, "fresh-token", None)
        self.assertEqual(
            case.headers,
            {"X-Existing": "preserved", "Authorization": "Bearer fresh-token"},
        )


if __name__ == "__main__":
    unittest.main()
