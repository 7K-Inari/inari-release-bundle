package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type APIClient struct {
	BaseDomain string
	KCURL      string
	User       string
	Password   string
	HTTP       *http.Client
}

func NewAPIClient(cfg Config) *APIClient {
	return &APIClient{
		BaseDomain: cfg.BaseDomain,
		KCURL:      cfg.KCURL,
		User:       cfg.User,
		Password:   cfg.Password,
		HTTP:       &http.Client{Timeout: 30 * time.Second},
	}
}

type Tenant struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	KeycloakOrgID string `json:"keycloakOrgId"`
}

type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Status, truncate(e.Body, 500))
}

func (a *APIClient) client() *http.Client {
	if a.HTTP == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	return a.HTTP
}

func (a *APIClient) Token(ctx context.Context) (string, error) {
	form := url.Values{
		"grant_type": {"password"},
		"client_id":  {"inari-server"},
		"username":   {a.User},
		"password":   {a.Password},
		"scope":      {"openid organization:*"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.KCURL+"/realms/inari/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := a.client().Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", &StatusError{Status: res.StatusCode, Body: string(raw)}
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("Keycloak token response has no access_token")
	}
	return body.AccessToken, nil
}

func (a *APIClient) do(ctx context.Context, method, path, token string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+a.BaseDomain+"/api/v1"+path, body)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := a.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, raw, err
}

func (a *APIClient) UnauthenticatedTenants(ctx context.Context) (int, error) {
	status, raw, err := a.do(ctx, http.MethodGet, "/tenants", "", nil)
	if err != nil {
		return 0, err
	}
	if status != http.StatusUnauthorized {
		return status, &StatusError{Status: status, Body: string(raw)}
	}
	return status, nil
}

func (a *APIClient) KeycloakDiscovery(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.KCURL+"/realms/inari/.well-known/openid-configuration", nil)
	if err != nil {
		return err
	}
	res, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return &StatusError{Status: res.StatusCode, Body: string(raw)}
	}
	var body struct {
		Issuer string `json:"issuer"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	if body.Issuer != a.KCURL+"/realms/inari" {
		return fmt.Errorf("Keycloak issuer=%q", body.Issuer)
	}
	return nil
}

func (a *APIClient) CreateTenant(ctx context.Context, token, slug string) (Tenant, error) {
	var tenant Tenant
	status, raw, err := a.do(ctx, http.MethodPost, "/tenants", token, map[string]string{
		"slug": slug, "displayName": "E2E Chaos " + slug,
	})
	if err != nil {
		return tenant, err
	}
	if status < 200 || status >= 300 {
		return tenant, &StatusError{Status: status, Body: string(raw)}
	}
	var body struct {
		Organization Tenant `json:"organization"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return tenant, err
	}
	if body.Organization.ID == "" || body.Organization.Slug != slug {
		return tenant, fmt.Errorf("tenant create response missing expected organization id/slug")
	}
	return body.Organization, nil
}

func (a *APIClient) GetTenant(ctx context.Context, token, slug string) (Tenant, int, error) {
	var tenant Tenant
	status, raw, err := a.do(ctx, http.MethodGet, "/tenants/"+slug, token, nil)
	if err != nil {
		return tenant, 0, err
	}
	if status < 200 || status >= 300 {
		return tenant, status, &StatusError{Status: status, Body: string(raw)}
	}
	var body struct {
		Organization Tenant `json:"organization"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return tenant, status, err
	}
	if body.Organization.ID == "" || body.Organization.Slug != slug {
		return tenant, status, fmt.Errorf("tenant read returned the wrong organization")
	}
	return body.Organization, status, nil
}

// WaitTenantReadable polls through asynchronous outbox/tuple materialization
// and returns a fresh, prevalidated token for a subsequent protected read.
func WaitTenantReadable(ctx context.Context, api *APIClient, slug string) (Tenant, string, error) {
	deadline := time.Now().Add(2 * time.Minute)
	var last error
	for time.Now().Before(deadline) {
		token, err := api.Token(ctx)
		if err == nil {
			tenant, _, readErr := api.GetTenant(ctx, token, slug)
			if readErr == nil {
				return tenant, token, nil
			}
			last = readErr
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return Tenant{}, "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
	return Tenant{}, "", fmt.Errorf("tenant %s did not become readable: %w", slug, last)
}
