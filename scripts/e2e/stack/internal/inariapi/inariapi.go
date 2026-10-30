// Package inariapi is a hand-rolled inari-server REST client. It drives the
// DEPLOYED server over HTTP from the toolbox pod — the suite never imports
// inari-server modules (OpenAPI conformance is api/'s job, not this one).
package inariapi

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
)

// Client targets http://inari-server:8080/api/v1 (cluster-internal DNS).
// Token mints a fresh bearer token per call (tokens are cheap; the script
// does the same so expiry mid-scenario is impossible).
type Client struct {
	NS      string
	Toolbox string
	Tenant  string
	Token   func() (string, error)
}

func (c *Client) base() string { return "http://inari-server:8080/api/v1" }

// do issues one authenticated request and returns the raw body.
func (c *Client) do(method, path string, body any) ([]byte, error) {
	tok, err := c.Token()
	if err != nil {
		return nil, fmt.Errorf("minting token: %w", err)
	}
	args := []string{"-X", method, "-H", "Authorization: Bearer " + tok}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		args = append(args, "-H", "Content-Type: application/json", "-d", string(raw))
	}
	args = append(args, c.base()+path)
	out, err := kube.Curl(c.NS, c.Toolbox, args...)
	if err != nil {
		return nil, err
	}
	return []byte(out), nil
}

// DoRaw issues one authenticated request WITHOUT curl -f, returning the
// HTTP status code and raw body — for assertions on error responses (e.g.
// the addons 422 schema-rejection contract), where do() would lose the
// body to curl's exit 22.
func (c *Client) DoRaw(method, path string, body any) (int, string, error) {
	tok, err := c.Token()
	if err != nil {
		return 0, "", fmt.Errorf("minting token: %w", err)
	}
	args := []string{"curl", "-s", "-m", "20", "-w", "\n%{http_code}",
		"-X", method, "-H", "Authorization: Bearer " + tok}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		args = append(args, "-H", "Content-Type: application/json", "-d", string(raw))
	}
	args = append(args, c.base()+path)
	out, err := kube.ExecInPod(c.NS, c.Toolbox, args...)
	if err != nil {
		return 0, "", err
	}
	idx := strings.LastIndex(out, "\n")
	if idx < 0 {
		return 0, "", fmt.Errorf("curl output lacks status line: %q", out)
	}
	code, err := strconv.Atoi(strings.TrimSpace(out[idx+1:]))
	if err != nil {
		return 0, "", fmt.Errorf("parsing curl status from %q: %w", out, err)
	}
	return code, out[:idx], nil
}

// SetPlatformFeatureFlag sets a platform-scoped runtime feature flag
// (PUT /api/v1/platform/feature-flags/{key}; requires org_creator).
func (c *Client) SetPlatformFeatureFlag(key string, value bool) (int, string, error) {
	return c.DoRaw("PUT", "/platform/feature-flags/"+key, map[string]any{"value": value})
}

// ReconcilePlatformResources triggers the platform-resources reconcile for
// the suite tenant (re-render + commit of tenant manifests, resync notify).
func (c *Client) ReconcilePlatformResources() (int, string, error) {
	return c.DoRaw("POST", "/tenants/"+c.Tenant+"/platform-resources/reconcile", nil)
}

// PlatformResource is the Resources Inventory row shape the suite asserts
// against (subset of the server's resourceView).
type PlatformResource struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// ListPlatformResources returns the suite tenant's Resources Inventory rows.
func (c *Client) ListPlatformResources() ([]PlatformResource, error) {
	var out struct {
		Resources []PlatformResource `json:"resources"`
	}
	if err := c.GetJSON("/tenants/"+c.Tenant+"/platform-resources", &out); err != nil {
		return nil, err
	}
	return out.Resources, nil
}

// AdoptAddon posts an adopt request, returning the raw status/body so both
// the 422 rejection and the success contract are assertable.
func (c *Client) AdoptAddon(clusterID, catalogItem string, payload map[string]any) (int, string, error) {
	return c.DoRaw("POST", "/tenants/"+c.Tenant+"/clusters/"+clusterID+"/addons/"+catalogItem+"/adopt", payload)
}

// DeleteAddon orphans an adopted addon (DELETE /api/v1/tenants/{org}/addons/{id})
// — cleanup for the adopt control case.
func (c *Client) DeleteAddon(addonID string) (int, string, error) {
	return c.DoRaw("DELETE", "/tenants/"+c.Tenant+"/addons/"+addonID, nil)
}

// GetJSON issues GET and unmarshals into out.
func (c *Client) GetJSON(path string, out any) error {
	body, err := c.do("GET", path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// PostJSON issues POST and unmarshals into out (out may be nil).
func (c *Client) PostJSON(path string, in, out any) error {
	body, err := c.do("POST", path, in)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// PutJSON issues PUT (no response body expected).
func (c *Client) PutJSON(path string, in any) error {
	_, err := c.do("PUT", path, in)
	return err
}

// --- typed helpers (golden-path surface only) ---

// CreateTenant creates the tenant and returns the organization's Keycloak
// org id (provisioning — phase 3 port from golden-path.sh).
func (c *Client) CreateTenant(slug, displayName string) (string, error) {
	var resp struct {
		Organization struct {
			KeycloakOrgID string `json:"keycloakOrgId"`
		} `json:"organization"`
	}
	err := c.PostJSON("/tenants", map[string]string{
		"slug": slug, "displayName": displayName,
	}, &resp)
	return resp.Organization.KeycloakOrgID, err
}

// RegisterCluster registers a cluster under the tenant and returns the
// cluster id and orgId (agent tenant id).
func (c *Client) RegisterCluster(name string, labels map[string]string) (clusterID, orgID string, err error) {
	var resp struct {
		Cluster Cluster `json:"cluster"`
	}
	err = c.PostJSON("/tenants/"+c.Tenant+"/clusters", map[string]any{
		"name": name, "labels": labels,
	}, &resp)
	return resp.Cluster.ID, resp.Cluster.OrgID, err
}

// IssueClusterToken mints a registration token for the cluster.
func (c *Client) IssueClusterToken(clusterID string) (string, error) {
	var resp struct {
		Token string `json:"token"`
	}
	err := c.PostJSON("/tenants/"+c.Tenant+"/clusters/"+clusterID+"/tokens", nil, &resp)
	return resp.Token, err
}

// MePermissions returns the caller's platform permissions.
func (c *Client) MePermissions() (struct {
	CanCreateOrganizations bool `json:"canCreateOrganizations"`
}, error,
) {
	var perms struct {
		CanCreateOrganizations bool `json:"canCreateOrganizations"`
	}
	err := c.GetJSON("/me/permissions", &perms)
	return perms, err
}

// Cluster is the subset of the cluster view the suite asserts on.
type Cluster struct {
	ID               string `json:"id"`
	OrgID            string `json:"orgId"`
	State            string `json:"state"`
	LastSeenAt       string `json:"lastSeenAt"`
	KeycloakClientID string `json:"keycloakClientId"`
}

// GetCluster fetches the cluster under the client's tenant.
func (c *Client) GetCluster(clusterID string) (Cluster, error) {
	var resp struct {
		Cluster Cluster `json:"cluster"`
	}
	err := c.GetJSON("/tenants/"+c.Tenant+"/clusters/"+clusterID, &resp)
	return resp.Cluster, err
}

// CapabilitiesLength returns how many capabilities the cluster streams.
func (c *Client) CapabilitiesLength(clusterID string) (int, error) {
	var resp struct {
		Capabilities []any `json:"capabilities"`
	}
	if err := c.GetJSON("/tenants/"+c.Tenant+"/clusters/"+clusterID+"/capabilities", &resp); err != nil {
		return 0, err
	}
	return len(resp.Capabilities), nil
}

// PutRBACMappings replaces the team's role mappings.
func (c *Client) PutRBACMappings(team, roleID string) error {
	return c.PutJSON("/tenants/"+c.Tenant+"/rbac/mappings", map[string]any{
		"mappings": []map[string]string{{"team": team, "roleId": roleID}},
	})
}

// CreatePolicy creates a policy and returns its id.
func (c *Client) CreatePolicy(name, target, engine, source string) (string, error) {
	var resp struct {
		Policy struct {
			ID string `json:"id"`
		} `json:"policy"`
	}
	err := c.PostJSON("/tenants/"+c.Tenant+"/policies", map[string]string{
		"name": name, "target": target, "engine": engine, "source": source,
	}, &resp)
	return resp.Policy.ID, err
}

// SetPolicyEnabled flips a policy's enabled flag (source must be resent).
func (c *Client) SetPolicyEnabled(policyID, source string, enabled bool) error {
	return c.PutJSON("/tenants/"+c.Tenant+"/policies/"+policyID, map[string]any{
		"source": source, "enabled": enabled,
	})
}

// Evaluation is the policy-evaluate decision the suite asserts on.
type Evaluation struct {
	Decision struct {
		Allow      bool `json:"allow"`
		Violations []struct {
			Rule string `json:"rule"`
		} `json:"violations"`
	} `json:"decision"`
}

// Evaluate runs POST /policies/evaluate for an image against the cluster.
func (c *Client) Evaluate(clusterID, image string) (Evaluation, error) {
	var eval Evaluation
	err := c.PostJSON("/tenants/"+c.Tenant+"/policies/evaluate", map[string]any{
		"itemId": "demo", "version": "1.0.0", "clusterId": clusterID,
		"spec": map[string]string{"image": image},
	}, &eval)
	return eval, err
}

// CreateScaffoldRun starts a go-service template run and returns its id.
func (c *Client) CreateScaffoldRun(values map[string]any) (string, error) {
	var resp struct {
		Run struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	err := c.PostJSON("/tenants/"+c.Tenant+"/templates/go-service/runs",
		map[string]any{"values": values}, &resp)
	return resp.Run.ID, err
}

// ScaffoldRunPhase returns the run's current phase.
func (c *Client) ScaffoldRunPhase(runID string) (string, error) {
	var resp struct {
		Run struct {
			Phase string `json:"phase"`
		} `json:"run"`
	}
	err := c.GetJSON("/tenants/"+c.Tenant+"/scaffold-runs/"+runID, &resp)
	return resp.Run.Phase, err
}

// Metrics returns the /metrics body of one server endpoint (host:port).
func (c *Client) Metrics(endpoint string) (string, error) {
	return kube.Curl(c.NS, c.Toolbox, "http://"+endpoint+"/metrics")
}
