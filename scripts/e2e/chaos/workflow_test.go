package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func repoFile(t *testing.T, parts ...string) string {
	t.Helper()
	all := append([]string{"..", "..", ".."}, parts...)
	raw, err := os.ReadFile(filepath.Join(all...))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestWeeklyCronRemainsLoadOnly(t *testing.T) {
	cron := repoFile(t, "gitops", "e2e", "argo-workflows", "cronworkflow-weekly.yaml")
	if !regexp.MustCompile(`(?m)- name: weekly-suite\s+value: load$`).MatchString(cron) {
		t.Fatalf("weekly cron must stay load-only until activation gates:\n%s", cron)
	}
	if !strings.Contains(cron, "reset is still a suspended placeholder") || !strings.Contains(cron, "recurring chaos is\n# NOT live") {
		t.Fatal("cron does not record the unmet activation gate")
	}
	reset := repoFile(t, "gitops", "e2e", "argo-workflows", "cronworkflow-nightly-reset.yaml")
	if !strings.Contains(reset, "suspend: true") {
		t.Fatal("reset unexpectedly enabled; activation evidence must be reviewed explicitly")
	}
}

func TestChaosDAGSelectionAndOrdering(t *testing.T) {
	workflow := repoFile(t, "gitops", "e2e", "argo-workflows", "workflowtemplate-e2e-stack.yaml")
	if strings.Count(workflow, "template: k6-run") != 3 {
		t.Fatal("k6 leg count changed")
	}
	loadGuard := `{{workflow.parameters.mode}} == weekly && ({{workflow.parameters.weekly-suite}} == load || {{workflow.parameters.weekly-suite}} == all)`
	if strings.Count(workflow, loadGuard) != 3 {
		t.Fatal("every k6 leg must require weekly-suite load|all")
	}
	for _, scenario := range []string{"cnpg-primary-delete", "keycloak-latency", "nats-outage", "openfga-latency", "server-pod-delete", "negative-control"} {
		if !strings.Contains(workflow, "name: chaos-"+scenario) || !strings.Contains(workflow, "chaos/"+scenario+"/junit.xml") {
			t.Errorf("scenario %s lacks DAG/reporting wiring", scenario)
		}
	}
	for _, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, "chaos-scenario}} == negative-control") && strings.Contains(line, "chaos-scenario}} == all ||") {
			t.Fatalf("negative control can be selected by all: %s", line)
		}
	}
	for _, dependency := range []string{
		"k6-provisioning-outbox.Succeeded || k6-provisioning-outbox.Skipped",
		"chaos-cnpg-primary-delete.Succeeded || chaos-cnpg-primary-delete.Skipped",
		"chaos-keycloak-latency.Succeeded || chaos-keycloak-latency.Skipped",
		"chaos-nats-outage.Succeeded || chaos-nats-outage.Skipped",
		"chaos-openfga-latency.Succeeded || chaos-openfga-latency.Skipped",
	} {
		if !strings.Contains(workflow, dependency) {
			t.Errorf("missing serialization dependency %s", dependency)
		}
	}
}

func TestNoReleaseOrNightlyChaosSelector(t *testing.T) {
	for _, path := range [][]string{
		{".github", "workflows", "release-e2e.yaml"},
		{".github", "workflows", "e2e-nightly.yaml"},
		{"gitops", "e2e", "argo-workflows", "cronworkflow-nightly.yaml"},
		{"gitops", "e2e", "argo-workflows", "cronworkflow-upgrade.yaml"},
	} {
		content := repoFile(t, path...)
		if strings.Contains(content, "weekly-suite") || strings.Contains(content, "chaos-scenario") {
			t.Fatalf("%s unexpectedly selects chaos", filepath.Join(path...))
		}
	}
}

func TestChaosRBACIsNamespacedAndSecretless(t *testing.T) {
	rbac := repoFile(t, "gitops", "e2e", "argo-workflows", "chaos-rbac.yaml")
	for _, forbidden := range []string{"kind: ClusterRole", "kind: ClusterRoleBinding", `resources: ["secrets"]`, `resources: ["nodes"]`, `resources: ["namespaces"]`, `resources: ["customresourcedefinitions"]`} {
		if strings.Contains(rbac, forbidden) {
			t.Fatalf("forbidden RBAC %q", forbidden)
		}
	}
}

func TestChaosUsesExistingGinkgoIdentityAndPrecompiledBinary(t *testing.T) {
	rbac := repoFile(t, "gitops", "e2e", "argo-workflows", "chaos-rbac.yaml")
	if strings.Contains(rbac, "kind: ServiceAccount\nmetadata:\n  name: e2e-chaos-driver") {
		t.Fatal("a second broad Ginkgo identity was introduced instead of reusing e2e-runner")
	}
	if !regexp.MustCompile(`(?s)name: e2e-chaos-driver\nsubjects:.*name: e2e-runner`).MatchString(rbac) {
		t.Fatal("driver Role is not bound to the existing e2e-runner identity")
	}
	workflow := repoFile(t, "gitops", "e2e", "argo-workflows", "workflowtemplate-e2e-stack.yaml")
	for _, want := range []string{"go test -c -tags=e2e -o /tools/stack.test .", `"serviceAccountName":"e2e-runner"`, "name: GINKGO_BINARY"} {
		if !strings.Contains(workflow, want) {
			t.Errorf("workflow missing amended Ginkgo requirement %q", want)
		}
	}
}

func TestCNPGTopologyPrerequisite(t *testing.T) {
	platform := repoFile(t, "gitops", "e2e", "env", "platform-config.yaml")
	if !regexp.MustCompile(`(?m)^\s+instances: 3$`).MatchString(platform) {
		t.Fatal("e2e CNPG must have three instances before failover testing")
	}
}
