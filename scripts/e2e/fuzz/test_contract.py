"""Contract tests for the W8 nightly Schemathesis leg (stdlib only)."""

from pathlib import Path
import tomllib
import unittest

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
CONFIG_PATH = HERE / "schemathesis.toml"
WORKFLOW_PATH = ROOT / "gitops/e2e/argo-workflows/workflowtemplate-e2e-stack.yaml"


class SchemathesisConfigContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        with CONFIG_PATH.open("rb") as config_file:
            cls.config = tomllib.load(config_file)
        cls.operations = cls.config.get("operations", [])

    def test_only_fuzzing_phase_and_no_response_driven_targeting(self) -> None:
        phases = self.config["phases"]
        self.assertTrue(phases["fuzzing"]["enabled"])
        for phase in ("examples", "coverage", "stateful"):
            self.assertFalse(phases[phase]["enabled"])
        self.assertEqual(phases["coverage"]["unexpected-methods"], [])
        self.assertFalse(phases["fuzzing"]["extra-data-sources"]["responses"])

    def test_delete_is_globally_disabled(self) -> None:
        self.assertTrue(
            any(
                operation.get("include-method") == "DELETE"
                and operation.get("enabled") is False
                for operation in self.operations
            )
        )

    def test_seeded_membership_team_and_rbac_mutations_are_disabled(self) -> None:
        marker = "(members|roles|rbac|teams)"
        blocks = [
            operation
            for operation in self.operations
            if marker in operation.get("include-name-regex", "")
        ]
        self.assertEqual(len(blocks), 1)
        self.assertTrue(blocks[0]["include-name-regex"].startswith("^(POST|PUT|PATCH)"))
        self.assertFalse(blocks[0]["enabled"])

    def test_global_catalog_sync_is_disabled(self) -> None:
        self.assertTrue(
            any(
                operation.get("include-name-regex")
                == "^POST /api/v1/admin/catalog/sync$"
                and operation.get("enabled") is False
                for operation in self.operations
            )
        )

    def test_disabled_blocks_never_or_path_and_method_filters(self) -> None:
        for operation in self.operations:
            if operation.get("enabled") is not False:
                continue
            includes = [key for key in operation if key.startswith("include-")]
            self.assertEqual(
                len(includes),
                1,
                f"disabled block has OR-broadening include filters: {includes}",
            )

    def test_all_tenant_writes_are_scoped_to_the_run_owned_org(self) -> None:
        self.assertEqual(
            self.config["parameters"]["path.org"], "${E2E_FUZZ_ORG}"
        )
        create = next(
            operation
            for operation in self.operations
            if operation.get("include-operation-id") == "createTenant"
        )
        self.assertFalse(create["enabled"])
        self.assertNotIn("parameters", create)
        self.assertNotIn('= "e2e-org"', CONFIG_PATH.read_text())

    def test_only_500_and_response_schema_checks_are_enabled(self) -> None:
        checks = self.config["checks"]
        self.assertFalse(checks["enabled"])
        self.assertTrue(checks["not_a_server_error"]["enabled"])
        self.assertEqual(
            checks["not_a_server_error"]["expected-statuses"],
            ["2xx", "3xx", "4xx", 501],
        )
        self.assertTrue(checks["response_schema_conformance"]["enabled"])
        self.assertFalse(checks["status_code_conformance"]["enabled"])

    def test_budget_network_and_auth_are_bounded(self) -> None:
        self.assertEqual(self.config["max-time"], 1200)
        self.assertEqual(self.config["max-failures"], 25)
        self.assertTrue(self.config["continue-on-failure"])
        self.assertEqual(self.config["workers"], 1)
        self.assertEqual(self.config["generation"]["max-examples"], 25)
        self.assertEqual(self.config["generation"]["database"], ":memory:")
        self.assertEqual(self.config["request-timeout"], 10.0)
        self.assertEqual(self.config["request-retries"], 2)
        self.assertEqual(self.config["rate-limit"], "10/s")
        self.assertEqual(self.config["base-url"], "https://${E2E_BASE_DOMAIN}")
        self.assertEqual(
            self.config["headers"]["Authorization"], "Bearer ${E2E_TOKEN}"
        )

    def test_keycloak_auth_refreshes_proactively_and_on_401(self) -> None:
        self.assertEqual(self.config["hooks"], "keycloak_auth")
        self.assertTrue((HERE / "keycloak_auth.py").is_file())
        self.assertIn("PYTHONPATH", (HERE / "run.sh").read_text())

    def test_junit_replay_reports_and_sanitization_are_wired(self) -> None:
        reports = self.config["reports"]
        self.assertEqual(reports["directory"], "/artifacts/report")
        self.assertEqual(reports["junit"]["path"], "/artifacts/out/junit.xml")
        self.assertEqual(reports["vcr"]["path"], "/artifacts/report/cassette.yaml")
        self.assertEqual(reports["json"]["path"], "/artifacts/report/run.json")
        self.assertEqual(self.config["cache"]["directory"], "/artifacts/report/cache")
        self.assertTrue(self.config["output"]["sanitization"]["enabled"])


class WorkflowContract(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = WORKFLOW_PATH.read_text()

    def test_fuzz_runs_nightly_after_both_suite_outcomes(self) -> None:
        self.assertIn(
            """          - name: fuzz
            template: fuzz
            depends: (ginkgo.Succeeded || ginkgo.Failed || ginkgo.Errored) && (playwright.Succeeded || playwright.Failed || playwright.Errored)
            when: "{{workflow.parameters.mode}} == nightly"
""",
            self.workflow,
        )

    def test_fuzz_template_has_its_own_bounded_deadline(self) -> None:
        self.assertIn(
            "    - name: fuzz\n      activeDeadlineSeconds: 2400\n",
            self.workflow,
        )
        self.assertIn("  activeDeadlineSeconds: 10800\n", self.workflow)

    def test_spec_tag_matches_the_deployed_server_without_fallback(self) -> None:
        self.assertIn(
            "ghcr.io/7k-inari/inari-server-openapi:{{workflow.parameters.server-image}}",
            self.workflow,
        )
        self.assertNotIn("inari-server-openapi:edge", self.workflow)
        self.assertIn("/spec/dist/openapi.yaml", self.workflow)

    def test_official_images_are_version_and_digest_pinned(self) -> None:
        self.assertIn(
            "schemathesis/schemathesis:4.33.4@sha256:708d934c8208a8f785bdf1b1610476e691f1efda0b7a3cb9ce7536d0f3280eec",
            self.workflow,
        )
        self.assertIn(
            "ghcr.io/oras-project/oras:v1.3.6@sha256:9cfb578be975b762fd898b9d004243d8003aeb807a3e15b2ed212cdd1326c9eb",
            self.workflow,
        )

    def test_fuzz_junit_and_replay_artifacts_reach_s3_and_exit_handler(self) -> None:
        self.assertIn("key: e2e/{{workflow.uid}}/fuzz/junit.xml", self.workflow)
        self.assertIn("key: e2e/{{workflow.uid}}/fuzz/report", self.workflow)
        self.assertIn("path: /artifacts/report", self.workflow)
        self.assertEqual(self.workflow.count("- name: fuzz-junit"), 1)
        self.assertIn("path: /junit/fuzz-junit.xml", self.workflow)

    def test_nonroot_schemathesis_can_write_its_volumes(self) -> None:
        self.assertIn(
            '''podSpecPatch: '{"securityContext":{"fsGroup":1000,"fsGroupChangePolicy":"OnRootMismatch"}}''',
            self.workflow,
        )

    def test_artifacts_volume_is_mounted_for_the_nonroot_runner(self) -> None:
        self.assertIn("        - name: artifacts\n          emptyDir: {}\n", self.workflow)
        self.assertIn("          - name: artifacts\n            mountPath: /artifacts\n", self.workflow)

    def test_runner_uses_the_checked_in_config_and_token_helper(self) -> None:
        self.assertIn("scripts/e2e/fuzz/run.sh", self.workflow)
        self.assertTrue((HERE / "ensure_tenant.py").is_file())
        self.assertIn("key: dev-admin-password", self.workflow)
        self.assertIn("e2e-{{workflow.uid}}-fuzz", self.workflow)


if __name__ == "__main__":
    unittest.main()
