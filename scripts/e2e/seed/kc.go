// Thin Keycloak admin/token client (stdlib net/http only) for the realm
// `inari`. Ported from the retiring scripts/e2e/ui/seed/seed-personas.mjs.
//
// One URL serves everything: on the live env the browser-reachable URL IS
// the API-reachable URL (Keycloak hostname v2 pins the issuer to the
// configured hostname, so tokens minted here carry the public iss
// regardless of which endpoint answered — verified against the e2e env).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// KC talks to the admin + token endpoints of one realm.
type KC struct {
	Base  string // e.g. https://auth-e2e.7kgroup.org
	Realm string // e.g. inari
	HTTP  *http.Client
}

func NewKC(base, realm string) *KC {
	return &KC{
		Base:  strings.TrimRight(base, "/"),
		Realm: realm,
		HTTP:  &http.Client{Timeout: 30 * time.Second},
	}
}

// api issues one admin-API request. A nil body means no request body; any
// non-nil body is JSON-encoded (a string body becomes a bare JSON string —
// KC's org-member join takes exactly that). Returns the status and decoded
// body (nil for empty 200/201/204 responses).
func (c *KC) api(token, method, path string, body any) (int, any, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.Base+"/admin/realms/"+c.Realm+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, nil, err
	}
	if res.StatusCode >= 400 {
		return res.StatusCode, nil, fmt.Errorf("%s %s -> %d: %s", method, path, res.StatusCode, truncate(string(raw), 400))
	}
	if len(raw) == 0 {
		return res.StatusCode, nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return res.StatusCode, nil, fmt.Errorf("decoding %s %s response: %w", method, path, err)
	}
	return res.StatusCode, decoded, nil
}

// token fetches a token from the realm's token endpoint with the given form.
func (c *KC) token(form url.Values) (string, error) {
	res, err := c.HTTP.PostForm(c.Base+"/realms/"+c.Realm+"/protocol/openid-connect/token", form)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return "", fmt.Errorf("token request failed: %d: %s", res.StatusCode, truncate(string(raw), 400))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decoding token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token response carried no access_token")
	}
	return out.AccessToken, nil
}

// ClientCredentialsToken fetches a service-account token (the
// inari-platform-admin admin client).
func (c *KC) ClientCredentialsToken(clientID, secret string) (string, error) {
	return c.token(url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {clientID},
		"client_secret": {secret},
	})
}

// UserToken fetches a direct-grant token through the public inari-server
// client (directAccessGrantsEnabled + audience mapper come from the realm
// import; the organization scope comes from the seed's scope repair).
func (c *KC) UserToken(username, password string) (string, error) {
	return c.token(url.Values{
		"grant_type": {"password"},
		"client_id":  {"inari-server"},
		"username":   {username},
		"password":   {password},
		"scope":      {"openid organization:*"},
	})
}

// GetClient returns the FULL client representation (needed for the
// full-replace PUT) for a clientId, or nil when absent.
func (c *KC) GetClient(adminToken, clientID string) (map[string]any, error) {
	_, decoded, err := c.api(adminToken, "GET", "/clients?clientId="+url.QueryEscape(clientID), nil)
	if err != nil {
		return nil, err
	}
	clients, _ := decoded.([]any)
	if len(clients) == 0 {
		return nil, nil
	}
	client, _ := clients[0].(map[string]any)
	return client, nil
}

// PutClient replaces a client (KC client update is full-replace — always
// send the merged full representation).
func (c *KC) PutClient(adminToken, id string, body map[string]any) error {
	_, _, err := c.api(adminToken, "PUT", "/clients/"+id, body)
	return err
}

// ClientScope is the subset of a realm client-scope the seed reads.
type ClientScope struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ListClientScopes returns the realm's client scopes.
func (c *KC) ListClientScopes(adminToken string) ([]ClientScope, error) {
	_, decoded, err := c.api(adminToken, "GET", "/client-scopes", nil)
	if err != nil {
		return nil, err
	}
	return decodeSlice[ClientScope](decoded)
}

// CreateClientScope creates a realm client scope; returns its id (from the
// Location header — the 201 body is empty).
func (c *KC) CreateClientScope(adminToken string, body map[string]any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", c.Base+"/admin/realms/"+c.Realm+"/client-scopes", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		msg, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("POST /client-scopes -> %d: %s", res.StatusCode, truncate(string(msg), 400))
	}
	loc := res.Header.Get("Location")
	id := loc[strings.LastIndex(loc, "/")+1:]
	if id == "" || id == loc {
		return "", fmt.Errorf("POST /client-scopes: 201 without a usable Location header %q", loc)
	}
	return id, nil
}

// AssignDefaultClientScope attaches a scope as a DEFAULT scope of a client.
func (c *KC) AssignDefaultClientScope(adminToken, clientUUID, scopeID string) error {
	_, _, err := c.api(adminToken, "PUT", "/clients/"+clientUUID+"/default-client-scopes/"+scopeID, nil)
	return err
}

// EnsureProtocolMapper adds a protocol mapper to a client by name; existing
// mapper (by name) = no-op. Returns true when created.
func (c *KC) EnsureProtocolMapper(adminToken, clientUUID, name string, body map[string]any) (bool, error) {
	client, err := c.getClientByUUID(adminToken, clientUUID)
	if err != nil {
		return false, err
	}
	for _, m := range stringSliceMaps(client["protocolMappers"]) {
		if m["name"] == name {
			return false, nil
		}
	}
	_, _, err = c.api(adminToken, "POST", "/clients/"+clientUUID+"/protocol-mappers/models", body)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *KC) getClientByUUID(adminToken, uuid string) (map[string]any, error) {
	_, decoded, err := c.api(adminToken, "GET", "/clients/"+uuid, nil)
	if err != nil {
		return nil, err
	}
	client, _ := decoded.(map[string]any)
	if client == nil {
		return nil, fmt.Errorf("GET /clients/%s returned a non-object", uuid)
	}
	return client, nil
}

// stringSliceMaps coerces a decoded JSON array to []map[string]any.
func stringSliceMaps(v any) []map[string]any {
	items, _ := v.([]any)
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// FindUser returns the user's id for an exact username, or "" when absent.
func (c *KC) FindUser(adminToken, username string) (string, error) {
	_, decoded, err := c.api(adminToken, "GET", "/users?username="+url.QueryEscape(username)+"&exact=true", nil)
	if err != nil {
		return "", err
	}
	users, _ := decoded.([]any)
	if len(users) == 0 {
		return "", nil
	}
	u, _ := users[0].(map[string]any)
	id, _ := u["id"].(string)
	return id, nil
}

// GetUser returns the full stored user representation (for read-merge-PUT).
func (c *KC) GetUser(adminToken, username string) (map[string]any, error) {
	_, decoded, err := c.api(adminToken, "GET", "/users?username="+url.QueryEscape(username)+"&exact=true", nil)
	if err != nil {
		return nil, err
	}
	users, _ := decoded.([]any)
	if len(users) == 0 {
		return nil, nil
	}
	u, _ := users[0].(map[string]any)
	return u, nil
}

// CreateUser creates a user; returns its id (from the Location header).
func (c *KC) CreateUser(adminToken string, body map[string]any) (string, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", c.Base+"/admin/realms/"+c.Realm+"/users", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 201 {
		msg, _ := io.ReadAll(res.Body)
		return "", fmt.Errorf("POST /users -> %d: %s", res.StatusCode, truncate(string(msg), 400))
	}
	loc := res.Header.Get("Location")
	id := loc[strings.LastIndex(loc, "/")+1:]
	if id == "" {
		return "", fmt.Errorf("POST /users: 201 without a usable Location header %q", loc)
	}
	return id, nil
}

// PutUser replaces a user (full-replace — send the merged stored
// representation so the declarative-profile attributes survive).
func (c *KC) PutUser(adminToken, id string, body map[string]any) error {
	_, _, err := c.api(adminToken, "PUT", "/users/"+id, body)
	return err
}

// ResetPassword converges the user's password credential (idempotent:
// personas carry exactly one password credential; Vault rotations
// propagate on the next seed run).
func (c *KC) ResetPassword(adminToken, id, password string) error {
	_, _, err := c.api(adminToken, "PUT", "/users/"+id+"/reset-password", map[string]any{
		"type": "password", "value": password, "temporary": false,
	})
	return err
}

// Group is the subset of a KC group the seed reads.
type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// FindGroup returns the id of a top-level group by name ("" when absent).
func (c *KC) FindGroup(adminToken, name string) (string, error) {
	_, decoded, err := c.api(adminToken, "GET", "/groups", nil)
	if err != nil {
		return "", err
	}
	groups, err := decodeSlice[Group](decoded)
	if err != nil {
		return "", err
	}
	for _, g := range groups {
		if g.Name == name {
			return g.ID, nil
		}
	}
	return "", nil
}

// AddUserToGroup joins a user to a group (idempotent — KC answers 204 on
// repeat joins).
func (c *KC) AddUserToGroup(adminToken, userID, groupID string) error {
	_, _, err := c.api(adminToken, "PUT", "/users/"+userID+"/groups/"+groupID, nil)
	return err
}

// JoinOrganization adds a user to a KC organization. KC 26.3 removed
// PUT .../members/{userId} (405): membership is POST .../members with the
// user ID as a JSON string body (201); a duplicate add answers 409 — that
// is success (idempotent).
func (c *KC) JoinOrganization(adminToken, orgID, userID string) error {
	status, _, err := c.api(adminToken, "POST", "/organizations/"+orgID+"/members", userID)
	if err != nil {
		if status == 409 {
			return nil
		}
		return err
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// decodeSlice re-marshals a decoded JSON array into the typed slice.
func decodeSlice[T any](decoded any) ([]T, error) {
	raw, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	var out []T
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
