//go:build e2e

package suite

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// Cluster phase helpers: registration aftermath — agent connect (cluster
// state active), ESO-projected OIDC client secret, capability stream,
// heartbeat freshness. The register/token calls and the agent helm install
// are provisioning (provide.go) — these are the observations.

// AssertClusterActive: cluster state advances via the agent gRPC stream
// (app-level); no k8s condition exists for it.
func AssertClusterActive(t TB, e *Env) {
	t.Helper()
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl, err := e.API.GetCluster(e.ClusterID)
		if err != nil {
			return false, nil // transient API/KC warm-up: keep polling
		}
		return cl.State == "active", nil
	}, "cluster active (agent connected; logs: kubectl -n inari-system logs deploy/inari-agent)")
}

// AssertESOAgentClientSecret: ESO sets Ready=True on the ExternalSecret once
// the Vault read + projection succeeds — an event-driven wait, not a blind
// poll. The projected secret must match the Keycloak client secret.
func AssertESOAgentClientSecret(t TB, e *Env) {
	t.Helper()
	require.NoError(t, kube.WaitCondition("inari-system", "condition=Ready",
		"120s", "externalsecret/inari-agent-oidc-client"),
		"ESO never projected inari-agent-oidc-client")
	secB64, err := kube.Kubectl("-n", "inari-system", "get", "secret",
		"inari-agent-oidc-client", "-o", "jsonpath={.data.client-secret}")
	require.NoError(t, err, "ExternalSecret Ready but secret missing")
	esoSecret, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secB64))
	require.NoError(t, err)

	cl, err := e.API.GetCluster(e.ClusterID)
	require.NoError(t, err)
	require.NotEmpty(t, cl.KeycloakClientID)
	adminTok, err := e.KC.AdminToken()
	require.NoError(t, err)
	kcSecret, err := e.KC.ClientSecret(adminTok, cl.KeycloakClientID)
	require.NoError(t, err)
	assert.Equal(t, kcSecret, string(esoSecret),
		"ESO-projected secret must match the Keycloak client secret")
}

// AssertCapabilitiesStreamed: capabilities arrive over the agent stream
// (app-level SSE).
func AssertCapabilitiesStreamed(t TB, e *Env) {
	t.Helper()
	var caps int
	var err error
	poll.Eventually(t, 180*time.Second, 5*time.Second, func() (bool, error) {
		caps, err = e.API.CapabilitiesLength(e.ClusterID)
		if err != nil {
			return false, nil
		}
		return caps > 0, nil
	}, "capabilities streamed (logs: kubectl -n inari-system logs deploy/inari-agent)")
	t.Logf("capabilities streamed: %d", caps)
}

// AssertHeartbeatAdvances: lastSeenAt must advance (throttled to 15s in the
// agentgateway). Poll, don't sleep: a stream reconnect mid-window
// legitimately stalls the next beat past a fixed 30s sleep.
func AssertHeartbeatAdvances(t TB, e *Env) {
	t.Helper()
	cl1, err := e.API.GetCluster(e.ClusterID)
	require.NoError(t, err)
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl2, err := e.API.GetCluster(e.ClusterID)
		if err != nil {
			return false, nil
		}
		return cl2.LastSeenAt != cl1.LastSeenAt, nil
	}, "heartbeat advancing (lastSeenAt changed; logs: kubectl -n inari-system logs deploy/inari-agent)")
}
