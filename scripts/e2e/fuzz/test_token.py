"""Tests for the credential-only Keycloak token helper."""

import io
import json
import os
from unittest.mock import patch
import unittest
from urllib.parse import parse_qs

from scripts.e2e.fuzz import fetch_token


class FakeResponse:
    def __init__(self, payload: dict) -> None:
        self.body = io.BytesIO(json.dumps(payload).encode())

    def __enter__(self):
        return self

    def __exit__(self, *_args) -> None:
        self.body.close()

    def read(self) -> bytes:
        return self.body.read()


class FetchTokenContract(unittest.TestCase):
    @patch.dict(
        os.environ,
        {
            "E2E_KC_URL": "https://auth.example.test/",
            "E2E_KC_REALM": "inari",
            "E2E_USER": "dev-admin",
            "E2E_PASSWORD": "dummy-password",
        },
        clear=True,
    )
    @patch("scripts.e2e.fuzz.fetch_token.urlopen")
    def test_password_grant_shape_and_token_extraction(self, mocked) -> None:
        mocked.return_value = FakeResponse({"access_token": "dummy-token"})
        self.assertEqual(fetch_token.fetch_token(), "dummy-token")

        request = mocked.call_args.args[0]
        self.assertEqual(
            request.full_url,
            "https://auth.example.test/realms/inari/protocol/openid-connect/token",
        )
        self.assertEqual(mocked.call_args.kwargs["timeout"], 15)
        self.assertEqual(
            parse_qs(request.data.decode()),
            {
                "grant_type": ["password"],
                "client_id": ["inari-server"],
                "username": ["dev-admin"],
                "password": ["dummy-password"],
                "scope": ["openid organization:*"],
            },
        )
        self.assertNotIn("dummy-password", request.full_url)

    @patch.dict(
        os.environ,
        {
            "E2E_KC_URL": "https://auth.example.test",
            "E2E_USER": "dev-admin",
            "E2E_PASSWORD": "dummy-password",
        },
        clear=True,
    )
    @patch("scripts.e2e.fuzz.fetch_token.urlopen")
    def test_missing_access_token_fails_closed(self, mocked) -> None:
        mocked.return_value = FakeResponse({"access_token": ""})
        with self.assertRaisesRegex(RuntimeError, "access_token"):
            fetch_token.fetch_token()

    @patch.dict(os.environ, {}, clear=True)
    def test_missing_environment_names_the_required_variable(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "E2E_KC_URL"):
            fetch_token.fetch_token()


if __name__ == "__main__":
    unittest.main()
