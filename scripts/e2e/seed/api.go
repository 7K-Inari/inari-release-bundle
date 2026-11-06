// Thin inari-server REST client (stdlib net/http only) for the seed. Role
// assignment goes through the real membership API — never KC groups, never
// direct DB — so seeding doubles as a membership-path P0 signal: a broken
// roles/members API fails the seed loudly.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// API targets the inari-server API base (…/api/v1).
type API struct {
	Base  string
	HTTP  *http.Client
	token string // cached user token (the seed runs well within the token TTL)
}

func NewAPI(base string) *API {
	return &API{Base: base, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// SetToken caches the dev-admin user token used for every call.
func (a *API) SetToken(token string) { a.token = token }

// StatusError carries the HTTP status for caller discrimination (404 =
// projection lag in the role-assignment poll; anything else = fail fast).
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%d: %s", e.Status, truncate(e.Body, 400))
}

func (a *API) call(method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.Base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return &StatusError{Status: res.StatusCode, Body: string(raw)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// Tenant is the subset of the organization view the seed reads.
type Tenant struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	KeycloakOrgID string `json:"keycloakOrgId"`
}

// ListTenants returns the caller-visible tenants.
func (a *API) ListTenants() ([]Tenant, error) {
	var out struct {
		Tenants []Tenant `json:"tenants"`
	}
	if err := a.call("GET", "/tenants", nil, &out); err != nil {
		return nil, err
	}
	return out.Tenants, nil
}

// CreateTenant creates the tenant and returns the organization's Keycloak
// org id (the response carries the full organization).
func (a *API) CreateTenant(slug, displayName string) (string, error) {
	var out struct {
		Organization Tenant `json:"organization"`
	}
	err := a.call("POST", "/tenants", map[string]string{
		"slug": slug, "displayName": displayName,
	}, &out)
	return out.Organization.KeycloakOrgID, err
}

// Cluster is the subset of the cluster view the seed reads.
type Cluster struct {
	ID    string `json:"id"`
	OrgID string `json:"orgId"`
	Name  string `json:"name"`
}

// ListClusters returns the tenant's registered clusters.
func (a *API) ListClusters(org string) ([]Cluster, error) {
	var out struct {
		Clusters []Cluster `json:"clusters"`
	}
	if err := a.call("GET", "/tenants/"+org+"/clusters", nil, &out); err != nil {
		return nil, err
	}
	return out.Clusters, nil
}

// RegisterCluster registers a cluster under the tenant and returns the
// cluster id and orgId (agent tenant id).
func (a *API) RegisterCluster(org, name string, labels map[string]string) (clusterID, orgID string, err error) {
	var out struct {
		Cluster Cluster `json:"cluster"`
	}
	err = a.call("POST", "/tenants/"+org+"/clusters", map[string]any{
		"name": name, "labels": labels,
	}, &out)
	return out.Cluster.ID, out.Cluster.OrgID, err
}

// ListRoles returns the tenant's roles (built-ins included).
func (a *API) ListRoles(org string) ([]Role, error) {
	var out struct {
		Roles []Role `json:"roles"`
	}
	if err := a.call("GET", "/tenants/"+org+"/roles", nil, &out); err != nil {
		return nil, err
	}
	return out.Roles, nil
}

// ListMembers returns the tenant's members with their role names.
func (a *API) ListMembers(org string) ([]Member, error) {
	var out struct {
		Members []Member `json:"members"`
	}
	if err := a.call("GET", "/tenants/"+org+"/members", nil, &out); err != nil {
		return nil, err
	}
	return out.Members, nil
}

// AssignRole grants a role to a member (PUT /tenants/{org}/members/{userId}
// {roleId}). A 404 means the server's users projection has not observed the
// KC org join yet — the caller retries until its deadline.
func (a *API) AssignRole(org, userID, roleID string) error {
	return a.call("PUT", "/tenants/"+org+"/members/"+userID, map[string]string{"roleId": roleID}, nil)
}

// GitConfig is the subset of the tenant git-config the seed reads.
type GitConfig struct {
	Repo         string `json:"repo"`
	CommitPolicy string `json:"commitPolicy"`
}

// GetGitConfig returns the tenant's git config, or (nil, nil) when unset
// (the server 404s "git config not set" until the first PUT; there is no
// DELETE — the git-config spec restores the ORIGINAL repo, so the seed
// ensures a stable default exactly once).
func (a *API) GetGitConfig(org string) (*GitConfig, error) {
	var out struct {
		Config *GitConfig `json:"config"`
	}
	err := a.call("GET", "/tenants/"+org+"/git-config", nil, &out)
	if err != nil {
		if se, ok := err.(*StatusError); ok && se.Status == 404 {
			return nil, nil
		}
		return nil, err
	}
	return out.Config, nil
}

// PutGitConfig sets the tenant's state repo (direct commits).
func (a *API) PutGitConfig(org, repo string) error {
	return a.call("PUT", "/tenants/"+org+"/git-config", map[string]string{
		"repo": repo, "commitPolicy": "direct",
	}, nil)
}
