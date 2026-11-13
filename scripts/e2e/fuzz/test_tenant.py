"""Contract tests for idempotent run-owned tenant setup."""

import io
import json
import os
from unittest.mock import patch
import unittest
from urllib.error import HTTPError
from urllib.parse import quote

from scripts.e2e.fuzz import ensure_tenant


class FakeResponse:
    def __init__(self, payload: dict | None = None) -> None:
        self.status = 200
        self.body = io.BytesIO(json.dumps(payload or {}).encode())

    def __enter__(self):
        return self

    def __exit__(self, *_args) -> None:
        self.body.close()

    def read(self) -> bytes:
        return self.body.read()


def http_error(request, status: int) -> HTTPError:
    return HTTPError(request.full_url, status, "test error", {}, None)


@patch.dict(
    os.environ,
    {"E2E_BASE_DOMAIN": "inari.example.test", "E2E_FUZZ_ORG": "e2e-run-fuzz"},
    clear=True,
)
class EnsureTenantContract(unittest.TestCase):
    @patch("scripts.e2e.fuzz.ensure_tenant.urlopen")
    def test_missing_tenant_is_created_once(self, mocked) -> None:
        mocked.return_value = FakeResponse({"slug": "e2e-run-fuzz"})
        self.assertEqual(ensure_tenant.ensure_tenant("dummy-token"), "created")
        create = mocked.call_args.args[0]
        self.assertEqual(create.get_method(), "POST")
        self.assertEqual(create.full_url, "https://inari.example.test/api/v1/tenants")
        self.assertEqual(
            json.loads(create.data),
            {"slug": "e2e-run-fuzz", "displayName": "E2E API fuzz e2e-run-fuzz"},
        )
        self.assertEqual(create.headers["Content-type"], "application/json")
        self.assertEqual(create.headers["Authorization"], "Bearer dummy-token")

    @patch("scripts.e2e.fuzz.ensure_tenant.urlopen")
    def test_duplicate_create_falls_back_to_existing_lookup(self, mocked) -> None:
        requests = []

        def respond(request, **_kwargs):
            requests.append(request)
            if request.get_method() == "POST":
                raise http_error(request, 500)
            return FakeResponse({"slug": "e2e-run-fuzz"})

        mocked.side_effect = respond
        self.assertEqual(ensure_tenant.ensure_tenant("dummy-token"), "existing")
        self.assertEqual([request.get_method() for request in requests], ["POST", "GET"])
        self.assertEqual(
            requests[-1].full_url,
            f"https://inari.example.test/api/v1/tenants/{quote('e2e-run-fuzz')}",
        )

    @patch("scripts.e2e.fuzz.ensure_tenant.urlopen")
    def test_create_error_without_existing_tenant_is_not_hidden(self, mocked) -> None:
        mocked.side_effect = lambda request, **_kwargs: (_ for _ in ()).throw(
            http_error(request, 500)
        )
        with self.assertRaisesRegex(RuntimeError, "HTTP 500"):
            ensure_tenant.ensure_tenant("dummy-token")
        self.assertEqual(mocked.call_count, 2)


if __name__ == "__main__":
    unittest.main()
