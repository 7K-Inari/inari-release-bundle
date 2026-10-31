//go:build e2e

package suite

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/poll"
)

// HA disruption helpers (pod kill, rollout under traffic, migration-lock
// race, leader-lease single-execution, agent stream fencing, agent leader
// failover). The specs at the module root gate them on E2E_HA=1; they run
// after the non-HA golden path has passed unchanged (replaces the script's
// INARI_HA branching with explicit skips).

const serverLabel = "app.kubernetes.io/name=inari-server"

func serverLogs(t TB, e *Env) string {
	t.Helper()
	out, err := kube.Logs(e.Namespace, serverLabel)
	require.NoError(t, err)
	return out
}

func countLinesContaining(s, sub string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			n++
		}
	}
	return n
}

// waitClusterActive polls until the registered cluster reports active.
func waitClusterActive(t TB, e *Env, msg string) {
	t.Helper()
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		cl, err := e.API.GetCluster(e.ClusterID)
		if err != nil {
			return false, nil
		}
		return cl.State == "active", nil
	}, msg)
}

// --- traffic sampler -------------------------------------------------------
// Background availability sampler probing /readyz through the ClusterIP
// Service from the toolbox pod — the exact readiness-gated routing clients
// depend on. Unauthenticated on purpose: Keycloak access tokens can expire
// mid-disruption and would measure token lifetime, not API availability. The
// sampler runs DETACHED inside the toolbox pod (kubectl exec drops the
// stream once stdin EOFs), writing ok=/fail= counts to /tmp/traffic.out.
const samplerScript = `#!/bin/sh
ok=0; fail=0; i=0
while [ "$i" -lt "$1" ]; do
  if curl -sf -m 5 -o /dev/null "$2"; then ok=$((ok+1)); else fail=$((fail+1)); fi
  i=$((i+1)); sleep 0.2
done
echo "ok=$ok fail=$fail"
`

func installSampler(t TB, e *Env) {
	t.Helper()
	_, err := kube.ExecInPodStdin(e.Namespace, e.Toolbox, samplerScript,
		"sh", "-c", "cat > /tmp/sampler.sh && chmod +x /tmp/sampler.sh")
	require.NoError(t, err)
}

// startTraffic launches the sampler for a window of `seconds` (≈5
// iterations/s — busybox sh has no SECONDS, so the window is an iteration
// count).
func startTraffic(t TB, e *Env, seconds int) {
	t.Helper()
	_, err := kube.ExecInPod(e.Namespace, e.Toolbox, "sh", "-c",
		fmt.Sprintf("rm -f /tmp/traffic.out; nohup /tmp/sampler.sh %d 'http://inari-server:8080/readyz' > /tmp/traffic.out 2>&1 &", seconds*5))
	require.NoError(t, err)
}

// stopTraffic waits out the sampler and returns the failed-request count.
func stopTraffic(t TB, e *Env) int {
	t.Helper()
	var out string
	poll.Eventually(t, 180*time.Second, 3*time.Second, func() (bool, error) {
		o, err := kube.ExecInPod(e.Namespace, e.Toolbox, "cat", "/tmp/traffic.out")
		if err != nil {
			return false, nil
		}
		out = o
		return strings.Contains(out, "fail="), nil
	}, "traffic sampler result")
	m := regexp.MustCompile(`ok=(\d+) fail=(\d+)`).FindStringSubmatch(out)
	require.NotNil(t, m, "unparseable sampler output: %q", out)
	fail, err := strconv.Atoi(m[2])
	require.NoError(t, err)
	t.Logf("traffic sample: %s", strings.TrimSpace(out))
	return fail
}

// --- HA(c): fresh 0→2 scale-up (migration-lock race) -----------------------
// The install IS the fresh 0→2 scale-up on a clean namespace: both replicas
// boot simultaneously against an empty database and the W1 migration
// advisory lock (ADR-0010) serializes their goose runs. The asserted outcome
// is the one the lock exists to guarantee: every migration applied exactly
// once and both replicas Ready. Boot-LOG evidence is deliberately NOT
// asserted (goose only logs the locker while migrations are pending; pods
// that restart during bring-up rotate logs away) — the lock mechanics are
// covered deterministically by W1's internal/db integration tests.
func HAMigrationLockRace(t TB, e *Env) {
	require.NoError(t, kube.WaitCondition(e.Namespace, "condition=ready", "300s",
		"pod", "-l", serverLabel),
		"HA(c): server pods never became ready")

	expected, err := filepath.Glob(filepath.Join(e.MigrationsDir, "[0-9]*.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, expected, "HA(c): no migrations under E2E_MIGRATIONS_DIR=%s", e.MigrationsDir)
	applied, err := kube.PSQLInari(e.Namespace, "SELECT max(version_id) FROM goose_db_version")
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(len(expected)), applied,
		"HA(c): concurrent first-boot migrations did not converge")

	// Soft CI evidence only: acquisition lines visible in the CURRENT logs.
	lockLines := countLinesContaining(serverLogs(t, e), "db: migration lock acquired")
	t.Logf("HA(c): migrations converged at version %s; advisory-lock acquisitions visible in current logs: %d (informational)",
		applied, lockLines)

	// The W1 chart knobs at replicaCount >= 2: PDB (minAvailable: 1) and
	// rollingUpdate maxUnavailable: 0.
	minAvail, err := kube.Kubectl("-n", e.Namespace, "get", "pdb", "inari-server",
		"-o", "jsonpath={.spec.minAvailable}")
	require.NoError(t, err, "HA(c): PodDisruptionBudget inari-server missing at replicaCount=2")
	assert.Equal(t, "1", strings.TrimSpace(minAvail), "HA(c): pdb minAvailable")
	maxUnavail, err := kube.Kubectl("-n", e.Namespace, "get", "deployment", "inari-server",
		"-o", "jsonpath={.spec.strategy.rollingUpdate.maxUnavailable}")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(maxUnavail), "0"),
		"HA(c): rollingUpdate.maxUnavailable is not 0 (got %q)", maxUnavail)
}

// --- HA(d1): leader-leased singleton loops run exactly once -----------------
// ADR-0011: each lease-gated loop (approvals-expiry, the group syncs, fleet
// loops, tzf-reconcile, ...) must have exactly one holder cluster-wide,
// renewed (expires_at in the future), stable across samples (no flapping),
// and — within this quiet window — acquired exactly once since boot.
func HALeaseSingleExecution(t TB, e *Env) {
	leases, err := kube.PSQLInari(e.Namespace, "SELECT name || '|' || holder FROM leader_leases")
	require.NoError(t, err)
	require.NotEmpty(t, leases, "HA(d1): leader_leases is empty (no lease-gated loop ever acquired?)")
	seen := map[string]bool{}
	for _, row := range strings.Split(leases, "\n") {
		name := strings.SplitN(row, "|", 2)[0]
		assert.False(t, seen[name], "HA(d1): lease %q has duplicate holders", name)
		seen[name] = true
	}
	for sample := 1; sample <= 2; sample++ {
		n, err := kube.PSQLInari(e.Namespace,
			"SELECT count(*) FROM leader_leases WHERE name='approvals-expiry' AND expires_at > now()")
		require.NoError(t, err)
		assert.Equal(t, "1", n, "HA(d1): approvals-expiry lease missing or not renewed (sample %d)", sample)
		if sample == 1 {
			time.Sleep(8 * time.Second)
		}
	}

	logs := serverLogs(t, e)
	acq := 0
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "leaderlease: acquired") && strings.Contains(line, "approvals-expiry") {
			acq++
		}
	}
	assert.Equal(t, 1, acq,
		"HA(d1): approvals-expiry leadership acquired %d times since boot, want exactly 1 (single-execution)", acq)

	// Every lease, not just approvals-expiry: exactly one acquisition since
	// boot per lease (no failover, no double-run). Log-based because the
	// live RBAC profile cannot read the leader_leases table directly.
	leaseRe := regexp.MustCompile(`leaderlease: acquired.*"lease":"([^"]*)"`)
	perLease := map[string]int{}
	for _, m := range leaseRe.FindAllStringSubmatch(logs, -1) {
		perLease[m[1]]++
	}
	require.NotEmpty(t, perLease, "HA(d1): no leaderlease acquisitions in server logs")
	for name, n := range perLease {
		assert.Equal(t, 1, n,
			"HA(d1): lease %q acquired %d times since boot (duplicate execution or flap)", name, n)
	}
}

// --- HA(d2): agent stream fencing evicts stale sessions ---------------------
// Fencing is per gateway instance (in-process session registry, W1), so the
// duplicate stream must land on the SAME server pod as the live one. Streams
// are per-connection load-balanced across the 2 server pods by the
// ClusterIP, so 3 agent pods guarantee a collision by pigeonhole (3 streams,
// 2 pods) — deterministic, no luck involved. The new stream wins; the stale
// session must be evicted (logged).
func HAStreamFencing(t TB, e *Env) {
	evictionsBefore := countLinesContaining(serverLogs(t, e), "evicting stale session")

	_, err := kube.Kubectl("-n", "inari-system", "scale", "deployment/inari-agent", "--replicas=3")
	require.NoError(t, err)
	fenced := false
	_ = poll.Until(120*time.Second, 5*time.Second, func() (bool, error) {
		logs, err := kube.Logs(e.Namespace, serverLabel)
		if err != nil {
			return false, nil
		}
		if countLinesContaining(logs, "evicting stale session") > evictionsBefore {
			fenced = true
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, kube.Scale("inari-system", "deployment/inari-agent", 1, "180s"))
	require.True(t, fenced,
		"HA(d2): no stale-session eviction was logged with 3 agent pods (fencing not exercised)")
	waitClusterActive(t, e, "HA(d2): cluster active after the agent scaled back to 1")
}

// --- HA(d3): agent leader failover within budget ----------------------------
// ACTIVE-PASSIVE agent HA (inari-agent W1): with 2 pods and leader election,
// killing the leader must promote a standby and restore the stream inside a
// bounded window. Live run 423ffd13 measured ~19s (kill → new lease holder);
// budget 60s to absorb kind CI noise.
func HAAgentLeaderFailover(t TB, e *Env) {
	// Leader election is enabled only here: HA(d2) above needs duplicate
	// streams, which leader election would suppress.
	_, err := kube.Run("helm", "upgrade", "--install", "inari-agent", e.AgentChartDir,
		"--namespace", "default", "--reuse-values",
		"--set", "leaderElection.enabled=true",
		"--wait", "--timeout", "180s")
	require.NoError(t, err, "HA(d3): enabling agent leader election")
	require.NoError(t, kube.Scale("inari-system", "deployment/inari-agent", 2, "180s"))

	holder := func() string {
		out, err := kube.Kubectl("-n", "inari-system", "get", "lease", "inari-agent.inari.dev",
			"-o", "jsonpath={.spec.holderIdentity}")
		if err != nil {
			return ""
		}
		h, _, _ := strings.Cut(strings.TrimSpace(out), "_")
		return h
	}
	leader := ""
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		leader = holder()
		return leader != "", nil
	}, "HA(d3): agent lease holder present (leaderElection.enabled not effective?)")
	t.Logf("HA(d3): leader pod: %s", leader)

	kill := time.Now()
	_, err = kube.Kubectl("-n", "inari-system", "delete", "pod", leader, "--wait=false")
	require.NoError(t, err)
	newLeader := ""
	poll.Eventually(t, 120*time.Second, 5*time.Second, func() (bool, error) {
		newLeader = holder()
		return newLeader != "" && newLeader != leader, nil
	}, "HA(d3): lease moved off the killed leader")
	failover := time.Since(kill)
	assert.LessOrEqual(t, failover, 60*time.Second,
		"HA(d3): leader failover took %s, over the 60s budget", failover.Round(time.Second))
	waitClusterActive(t, e, "HA(d3): cluster active after leader failover")
	t.Logf("HA(d3): failover %s (kill -> new leader), new leader: %s",
		failover.Round(time.Second), newLeader)
	require.NoError(t, kube.Scale("inari-system", "deployment/inari-agent", 1, "180s"))
}

// --- HA(a): delete one server pod mid-run -----------------------------------
// The surviving replica must keep serving (readiness-gated: zero failed
// probes), and the claim-based loops (outbox relay + scaffold reconcile —
// deliberately NOT lease-gated, ADR-0011) plus the durable JetStream
// consumer groups (ADR-0014) must keep processing work.
func HAPodKill(t TB, e *Env) {
	installSampler(t, e)
	victim, err := kube.Kubectl("-n", e.Namespace, "get", "pods", "-l", serverLabel,
		"-o", "jsonpath={.items[0].metadata.name}")
	require.NoError(t, err)
	victim = strings.TrimSpace(victim)
	t.Logf("HA(a): victim pod: %s", victim)

	startTraffic(t, e, 90)
	_, err = kube.Kubectl("-n", e.Namespace, "delete", "pod", victim, "--wait=false")
	require.NoError(t, err)
	require.NoError(t, kube.RolloutStatus(e.Namespace, "deployment/inari-server", "240s"))
	fails := stopTraffic(t, e)
	assert.Equal(t, 0, fails,
		"HA(a): %d failed requests while a pod was being replaced", fails)

	// The claim-based outbox relay must still materialize an RBAC mapping
	// flip into the tenant state repo after the pod loss.
	FlipRBACMapping(t, e, "viewers", "admin")

	// Restore the pre-disruption mapping (viewers→editor, set in the RBAC
	// phase): the console UI suite runs after this phase and its
	// built-in-protection spec requires exactly one admin-bearing team in
	// the org.
	FlipRBACMapping(t, e, "viewers", "editor")

	// Durable-consumer failover evidence: the shared outbox-rbac-materialize
	// group kept acking after the pod loss (ack floor advanced past the
	// pre-kill deliveries).
	consumerJSON, err := kube.ExecInPod(e.Namespace, "deploy/nats-box",
		"nats", "consumer", "info", "INARI_OUTBOX", "outbox-rbac-materialize",
		"--server", "nats:4222", "--json")
	require.NoError(t, err)
	var consumer struct {
		AckFloor struct {
			StreamSeq int `json:"stream_seq"`
		} `json:"ack_floor"`
		NumPending int `json:"num_pending"`
	}
	require.NoError(t, json.Unmarshal([]byte(consumerJSON), &consumer))
	assert.Positive(t, consumer.AckFloor.StreamSeq,
		"HA(a): outbox-rbac-materialize ack floor did not advance after failover")
	assert.Equal(t, 0, consumer.NumPending,
		"HA(a): outbox-rbac-materialize consumer did not drain after failover")

	// A fresh scaffold run must still reach completed on the survivor. The
	// go-service skeleton templates .Values.goVersion/.Values.port too; the
	// renderer does not inject schema defaults, so pass all four.
	runID, err := e.API.CreateScaffoldRun(map[string]any{
		"serviceName": "ha-probe", "module": "github.com/e2e/ha-probe",
		"goVersion": "1.23", "port": 8080,
	})
	require.NoError(t, err, "HA(a): scaffold run creation failed")
	require.NotEmpty(t, runID)
	poll.Eventually(t, 240*time.Second, 5*time.Second, func() (bool, error) {
		phase, err := e.API.ScaffoldRunPhase(runID)
		if err != nil {
			return false, nil
		}
		if phase == "failed" {
			return false, fmt.Errorf("HA(a): scaffold run failed post-disruption")
		}
		return phase == "completed", nil
	}, "HA(a): scaffold run completed (reconcile loop progressing on the survivor)")
}

// --- HA(b): rollout restart under traffic -----------------------------------
// A full rolling restart must honor rollingUpdate.maxUnavailable: 0
// (available replicas never drop below 2) and keep failed requests within a
// small error budget (in-flight connections may reset during pod
// termination).
func HARolloutRestart(t TB, e *Env) {
	installSampler(t, e)
	startTraffic(t, e, 150)

	// availableReplicas watcher: a transient kubectl/apiserver error yields
	// an empty sample — skip it rather than recording a bogus 0.
	minCh := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		min := 99
		for i := 0; i < 150; i++ {
			out, err := kube.Kubectl("-n", e.Namespace, "get", "deployment", "inari-server",
				"-o", "jsonpath={.status.availableReplicas}")
			if err == nil {
				if av, convErr := strconv.Atoi(strings.TrimSpace(out)); convErr == nil && av < min {
					min = av
				}
			}
			time.Sleep(time.Second)
		}
		minCh <- min
	}()

	_, err := kube.Kubectl("-n", e.Namespace, "rollout", "restart", "deployment/inari-server")
	require.NoError(t, err)
	require.NoError(t, kube.RolloutStatus(e.Namespace, "deployment/inari-server", "300s"))
	minAvail := <-minCh
	<-done
	fails := stopTraffic(t, e)

	assert.GreaterOrEqual(t, minAvail, 2,
		"HA(b): availableReplicas dropped to %d during the rollout (maxUnavailable: 0 violated)", minAvail)
	const errorBudget = 2
	assert.LessOrEqual(t, fails, errorBudget,
		"HA(b): %d failed requests during the rollout restart, over the error budget of %d", fails, errorBudget)
}
