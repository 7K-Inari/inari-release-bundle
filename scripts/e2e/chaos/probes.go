package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DomainState contains evidence only; Token is deliberately excluded from JSON.
type DomainState struct {
	Tenant       *Tenant        `json:"tenant,omitempty"`
	Token        string         `json:"-"`
	OldPrimary   string         `json:"oldPrimary,omitempty"`
	Backlog      int            `json:"backlog,omitempty"`
	TimeoutProbe *ProbeOutcome  `json:"timeoutProbe,omitempty"`
	Details      map[string]any `json:"details,omitempty"`
}

type ProbeOutcome struct {
	Status   int    `json:"status,omitempty"`
	TimedOut bool   `json:"timedOut,omitempty"`
	Duration string `json:"duration"`
	Outcome  string `json:"outcome"`
}

func scenarioSlug(cfg Config, suffix string) string {
	return "e2e-" + cfg.RunID + "-chaos-" + suffix
}

func (d *DomainState) createFixture(ctx context.Context, cfg Config, api *APIClient, suffix string) error {
	token, err := api.Token(ctx)
	if err != nil {
		return fmt.Errorf("direct grant before tenant create: %w", err)
	}
	tenant, err := api.CreateTenant(ctx, token, scenarioSlug(cfg, suffix))
	if err != nil {
		return err
	}
	readable, freshToken, err := WaitTenantReadable(ctx, api, tenant.Slug)
	if err != nil {
		return err
	}
	if readable.ID != tenant.ID {
		return fmt.Errorf("tenant id changed during readback")
	}
	d.Tenant = &readable
	d.Token = freshToken
	return nil
}

func currentPrimary(ctx context.Context, kube *KubeClient) (string, error) {
	raw, err := kube.GetJSON(ctx, "cluster.postgresql.cnpg.io", "postgresql")
	if err != nil {
		return "", err
	}
	var cluster struct {
		Spec struct {
			Instances int `json:"instances"`
		} `json:"spec"`
		Status struct {
			CurrentPrimary string `json:"currentPrimary"`
			ReadyInstances int    `json:"readyInstances"`
			Phase          string `json:"phase"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &cluster); err != nil {
		return "", err
	}
	return cluster.Status.CurrentPrimary, nil
}

func waitCNPGHealthy(ctx context.Context, kube *KubeClient, oldPrimary string, requireFailover bool) error {
	deadline := time.Now().Add(4 * time.Minute)
	var detail string
	for time.Now().Before(deadline) {
		raw, err := kube.GetJSON(ctx, "cluster.postgresql.cnpg.io", "postgresql")
		if err == nil {
			var cluster struct {
				Spec struct {
					Instances int `json:"instances"`
				} `json:"spec"`
				Status struct {
					CurrentPrimary string `json:"currentPrimary"`
					ReadyInstances int    `json:"readyInstances"`
					Phase          string `json:"phase"`
				} `json:"status"`
			}
			if json.Unmarshal(raw, &cluster) == nil {
				detail = fmt.Sprintf("instances=%d ready=%d phase=%s primary=%s", cluster.Spec.Instances, cluster.Status.ReadyInstances, cluster.Status.Phase, cluster.Status.CurrentPrimary)
				healthy := cluster.Spec.Instances == 3 && cluster.Status.ReadyInstances == 3 && cluster.Status.CurrentPrimary != "" && cluster.Status.Phase == "Cluster in healthy state"
				if healthy && (!requireFailover || cluster.Status.CurrentPrimary != oldPrimary) {
					return nil
				}
			}
		} else {
			detail = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("CNPG did not become healthy with three instances: %s", detail)
}

var sqlIdentifier = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func psqlInari(ctx context.Context, kube *KubeClient, query string) (string, error) {
	primary, err := currentPrimary(ctx, kube)
	if err != nil {
		return "", err
	}
	if primary == "" {
		return "", fmt.Errorf("CNPG primary is empty")
	}
	out, err := kube.Output(ctx, nil, "exec", primary, "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "inari", "-tAc", query)
	return strings.TrimSpace(out), err
}

func outboxCount(ctx context.Context, kube *KubeClient, orgID string, unpublished, errorsOnly bool) (int, error) {
	if !sqlIdentifier.MatchString(orgID) {
		return 0, fmt.Errorf("unsafe org id %q", orgID)
	}
	predicate := "published_at IS NULL"
	if !unpublished {
		predicate = "published_at IS NOT NULL"
	}
	if errorsOnly {
		predicate += " AND last_error <> ''"
	}
	out, err := psqlInari(ctx, kube, "SELECT count(*) FROM outbox WHERE org_id='"+orgID+"' AND "+predicate)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func organizationRowCount(ctx context.Context, kube *KubeClient, orgID string) (int, error) {
	if !sqlIdentifier.MatchString(orgID) {
		return 0, fmt.Errorf("unsafe org id %q", orgID)
	}
	out, err := psqlInari(ctx, kube, "SELECT count(*) FROM organizations WHERE id='"+orgID+"'")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func waitOutboxDrained(ctx context.Context, kube *KubeClient, orgID string) error {
	deadline := time.Now().Add(4 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		pending, err := outboxCount(ctx, kube, orgID, true, false)
		if err == nil && pending == 0 {
			dead, deadErr := outboxCount(ctx, kube, orgID, false, true)
			if deadErr == nil && dead == 0 {
				return nil
			}
			last = fmt.Errorf("dead-letter rows=%d err=%v", dead, deadErr)
		} else {
			last = fmt.Errorf("unpublished rows=%d err=%v", pending, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("org-scoped outbox did not drain cleanly: %w", last)
}

func waitServerReady(ctx context.Context, kube *KubeClient) error {
	deadline := time.Now().Add(4 * time.Minute)
	var detail string
	for time.Now().Before(deadline) {
		raw, err := kube.GetJSON(ctx, "deployment", "inari-server")
		if err == nil {
			var deployment struct {
				Spec struct {
					Replicas int `json:"replicas"`
				} `json:"spec"`
				Status struct {
					ReadyReplicas   int `json:"readyReplicas"`
					UpdatedReplicas int `json:"updatedReplicas"`
				} `json:"status"`
			}
			if json.Unmarshal(raw, &deployment) == nil {
				detail = fmt.Sprintf("spec=%d ready=%d updated=%d", deployment.Spec.Replicas, deployment.Status.ReadyReplicas, deployment.Status.UpdatedReplicas)
				if deployment.Spec.Replicas == 2 && deployment.Status.ReadyReplicas == 2 && deployment.Status.UpdatedReplicas == 2 {
					return nil
				}
			}
		} else {
			detail = err.Error()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return fmt.Errorf("inari-server did not return to two Ready replicas: %s", detail)
}

func prepareScenario(ctx context.Context, cfg Config, api *APIClient, kube *KubeClient, state *DomainState) error {
	switch cfg.Scenario.Name {
	case "cnpg-primary-delete":
		if err := waitCNPGHealthy(ctx, kube, "", false); err != nil {
			return err
		}
		primary, err := currentPrimary(ctx, kube)
		if err != nil {
			return err
		}
		state.OldPrimary = primary
		return state.createFixture(ctx, cfg, api, "cnpg")
	case "server-pod-delete":
		if err := waitServerReady(ctx, kube); err != nil {
			return err
		}
		return state.createFixture(ctx, cfg, api, "server")
	}
	return nil
}

// prepareOpenFGATimeout runs between the two OpenFGA engines. The 6s pause
// exceeds the configured 1–5s PEP cache TTL, so the timeout read cannot be
// satisfied by the prevalidation's cached allow.
func prepareOpenFGATimeout(ctx context.Context, cfg Config, api *APIClient, state *DomainState) error {
	if err := state.createFixture(ctx, cfg, api, "fga"); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(6 * time.Second):
		return nil
	}
}

func duringEngine(ctx context.Context, cfg Config, api *APIClient, kube *KubeClient, key string, state *DomainState) error {
	switch key {
	case "nats-outage":
		token, err := api.Token(ctx)
		if err != nil {
			return err
		}
		tenant, err := api.CreateTenant(ctx, token, scenarioSlug(cfg, "nats"))
		if err != nil {
			return fmt.Errorf("tenant creation was not accepted during the NATS outage: %w", err)
		}
		state.Tenant = &tenant
		deadline := time.Now().Add(45 * time.Second)
		for {
			pending, countErr := outboxCount(ctx, kube, tenant.ID, true, false)
			if countErr == nil && pending > 0 {
				state.Backlog = pending
				return nil
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("no unpublished outbox row for org %s during NATS outage (count=%d, err=%v)", tenant.ID, pending, countErr)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	case "openfga-timeout":
		if state.Tenant == nil || state.Token == "" {
			return fmt.Errorf("OpenFGA timeout fixture/token was not prevalidated")
		}
		start := time.Now()
		probeCtx, cancel := context.WithTimeout(ctx, 75*time.Second)
		_, status, err := api.GetTenant(probeCtx, state.Token, state.Tenant.Slug)
		cancel()
		outcome := &ProbeOutcome{Status: status, Duration: time.Since(start).Round(time.Millisecond).String()}
		state.TimeoutProbe = outcome
		switch {
		case status >= 200 && status < 300:
			outcome.Outcome = "unexpected-2xx"
			return fmt.Errorf("protected OpenFGA read returned HTTP %d during the 90s latency window", status)
		case status == http.StatusUnauthorized:
			outcome.Outcome = "stale-token-401"
			return fmt.Errorf("protected read returned 401; a stale token is not fail-closed evidence")
		case status == http.StatusForbidden || status >= 500:
			outcome.Outcome = "bounded-non-2xx"
			return nil
		case err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) || strings.Contains(err.Error(), "Client.Timeout")):
			outcome.TimedOut = true
			outcome.Outcome = "bounded-deadline"
			return nil
		default:
			outcome.Outcome = "unexpected-result"
			return fmt.Errorf("protected read did not fail closed as expected (status=%d, err=%v)", status, err)
		}
	case "negative-control":
		probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		status, err := api.UnauthenticatedTenants(probeCtx)
		if status >= 200 && status < 300 {
			return fmt.Errorf("negative control ineffective: unauthenticated API returned HTTP %d", status)
		}
		if err == nil && status == http.StatusUnauthorized {
			return fmt.Errorf("negative control ineffective: API still returned 401")
		}
		return nil
	case "keycloak-latency":
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, err := api.UnauthenticatedTenants(probeCtx); err != nil {
			return err
		}
		return api.KeycloakDiscovery(probeCtx)
	default:
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		_, err := api.UnauthenticatedTenants(probeCtx)
		return err
	}
}

func recoverScenario(ctx context.Context, cfg Config, api *APIClient, kube *KubeClient, state *DomainState) error {
	switch cfg.Scenario.Name {
	case "cnpg-primary-delete":
		if state.Tenant == nil {
			return fmt.Errorf("CNPG fixture was not created")
		}
		if err := waitCNPGHealthy(ctx, kube, state.OldPrimary, true); err != nil {
			return err
		}
		rows, err := organizationRowCount(ctx, kube, state.Tenant.ID)
		if err != nil || rows != 1 {
			return fmt.Errorf("tenant data survival rows=%d err=%v", rows, err)
		}
		_, _, err = WaitTenantReadable(ctx, api, state.Tenant.Slug)
		return err
	case "nats-outage":
		if state.Tenant == nil || state.Backlog < 1 {
			return fmt.Errorf("NATS org-scoped backlog was not observed")
		}
		if err := waitOutboxDrained(ctx, kube, state.Tenant.ID); err != nil {
			return err
		}
		_, _, err := WaitTenantReadable(ctx, api, state.Tenant.Slug)
		return err
	case "openfga-latency":
		if state.Tenant == nil {
			return fmt.Errorf("OpenFGA fixture was not created")
		}
		_, _, err := WaitTenantReadable(ctx, api, state.Tenant.Slug)
		return err
	case "server-pod-delete":
		if state.Tenant == nil {
			return fmt.Errorf("server fixture was not created")
		}
		if err := waitServerReady(ctx, kube); err != nil {
			return err
		}
		if err := waitOutboxDrained(ctx, kube, state.Tenant.ID); err != nil {
			return err
		}
		_, _, err := WaitTenantReadable(ctx, api, state.Tenant.Slug)
		return err
	case "negative-control":
		if err := waitServerReady(ctx, kube); err != nil {
			return err
		}
		_, err := api.UnauthenticatedTenants(ctx)
		return err
	case "keycloak-latency":
		if err := api.KeycloakDiscovery(ctx); err != nil {
			return err
		}
		_, err := api.UnauthenticatedTenants(ctx)
		return err
	}
	return nil
}

func (d *DomainState) WriteEvidence(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
