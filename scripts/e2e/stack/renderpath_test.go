//go:build e2e

package stack_test

import (
	. "github.com/onsi/ginkgo/v2"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/suite"
)

// Render-path validation (M5W2 + M5W3-2, operator-sunset Phases 1+3): the
// server-rendered native resources + KRO instance + provider-keycloak
// TenantRealm/TenantClient XRs sync Healthy via ArgoCD, realms/clients are
// created (and a disposable realm deleted) by provider-keycloak, client
// secrets land in the tenant namespace via the ESO delivery bundle, RGD
// teardown cascades, agent status streaming works on the new GVRs, and
// addon schema validation rejects bad values with 422. MANUAL-ONLY: the
// specs carry no priority label, so neither the release-gate filter
// ((p0||smoke)&&!quarantine) nor the nightly filter (p0||p1||p2) ever
// selects them — the flow is destructive (render cutover + RGD teardown)
// and must not interleave with the golden-path assertions. Run it
// explicitly against the golden-path stack (the suite provisions in-process
// when no handoff is configured, or attaches via E2E_HANDOFF_PATH /
// E2E_HANDOFF_CONFIGMAP):
//
//	KEEP_CLUSTER=true go test -tags=e2e -count=1 -timeout 45m . \
//	  -args --ginkgo.label-filter=renderpath
//
// The phases are stateful and strictly ordered (Ordered + BeforeAll-built
// shared state): a failed phase aborts the rest of the flow.
var _ = Describe("RenderPath", Label(suite.LabelRenderPath), Ordered, func() {
	var r *suite.RenderPath

	BeforeAll(func() {
		r = suite.NewRenderPath(GinkgoT(), env)
	})

	It("installs ArgoCD (side-loaded images + day-0 installer)", func() {
		r.ArgoCDInstall(GinkgoT())
	})
	It("serves the git hostPath over git:// for in-cluster ArgoCD", func() {
		r.GitDaemon(GinkgoT())
	})
	It("installs the platform-graphs chart (RGD Active, instance CRD established)", func() {
		r.PlatformGraphs(GinkgoT())
	})
	It("flips the gitops_render cutover flag and reconciles", func() {
		r.RenderCutover(GinkgoT())
	})
	It("commits the rendered descriptor layout to the platform repo", func() {
		r.RenderCommit(GinkgoT())
	})
	It("syncs the rendered child Applications to Synced/Healthy", func() {
		r.ArgoCDSync(GinkgoT())
	})
	It("enforces the rendered AppProject guardrails", func() {
		r.AppProjectEnforced(GinkgoT())
	})
	It("sees ExternalDNS accept the rendered DNSEndpoint", func() {
		r.DNSEndpointAccepted(GinkgoT())
	})
	It("materializes the KRO tenantnamespace children", func() {
		r.KROChildren(GinkgoT())
	})
	It("readies the harness ClusterIssuer (native-resource proof)", func() {
		r.ClusterIssuerFixture(GinkgoT())
	})
	It("reconciles the TenantRealm XR through provider-keycloak", func() {
		r.KeycloakRealmReady(GinkgoT())
	})
	It("delivers the TenantClient credentials Secret to the tenant namespace", func() {
		r.KeycloakClientSecret(GinkgoT())
	})
	It("tears down a disposable realm end-to-end (XR → MR → Keycloak)", func() {
		r.KeycloakTeardown(GinkgoT())
	})
	It("streams the new GVRs' status into the Resources Inventory", func() {
		r.StatusStreaming(GinkgoT())
	})
	It("rejects schema-invalid addon values with 422", func() {
		r.AddonSchema422(GinkgoT())
	})
	It("cascades the RGD teardown and recovers on recreate", func() {
		r.RGDTeardown(GinkgoT())
	})
})
