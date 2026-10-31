// Package kc is a thin Keycloak admin/token client. Every call runs curl
// inside the cluster's toolbox pod (same transport as golden-path.sh), so no
// port-forwards are needed.
package kc

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"7k-inari/inari-release-bundle/scripts/e2e/stack/internal/kube"
)

// Client talks to the realm `inari` on keycloak-service.<ns>.svc:8080.
type Client struct {
	NS      string // stack namespace (e.g. "inari")
	Toolbox string // toolbox pod name (curl host)
}

func (c *Client) base() string { return "http://keycloak-service:8080" }

func (c *Client) get(path, token string, out any) error {
	var args []string
	if token != "" {
		args = append(args, "-H", "Authorization: Bearer "+token)
	}
	args = append(args, c.base()+path)
	body, err := kube.Curl(c.NS, c.Toolbox, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(body), out)
}

// RealmIssuer returns the issuer advertised by the realm's OIDC discovery
// document. Issuer convergence after the hostname patch is observable ONLY
// this way — no k8s condition exists for it.
func (c *Client) RealmIssuer() (string, error) {
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if err := c.get("/realms/inari/.well-known/openid-configuration", "", &doc); err != nil {
		return "", err
	}
	return doc.Issuer, nil
}

func (c *Client) tokenEndpoint(form ...string) (string, error) {
	args := []string{c.base() + "/realms/inari/protocol/openid-connect/token"}
	for _, f := range form {
		args = append(args, "-d", f)
	}
	body, err := kube.Curl(c.NS, c.Toolbox, args...)
	if err != nil {
		return "", err
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return "", fmt.Errorf("decoding token response: %w (%s)", err, body)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("token response carried no access_token")
	}
	return resp.AccessToken, nil
}

// AdminToken fetches a client-credentials token for inari-platform-admin;
// the client secret comes from the inari-keycloak-admin k8s secret.
func (c *Client) AdminToken() (string, error) {
	secB64, err := kube.Kubectl("-n", c.NS, "get", "secret", "inari-keycloak-admin",
		"-o", "jsonpath={.data.client-secret}")
	if err != nil {
		return "", err
	}
	sec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(secB64))
	if err != nil {
		return "", fmt.Errorf("decoding inari-keycloak-admin client-secret: %w", err)
	}
	return c.tokenEndpoint(
		"grant_type=client_credentials",
		"client_id=inari-platform-admin",
		"client_secret="+string(sec),
	)
}

// MasterToken fetches a bootstrap-admin token against the master realm
// (keycloak-initial-admin secret). Cross-realm assertions (tenant realms
// created by provider-keycloak) need it: the inari-platform-admin client
// only holds realm-admin on the inari realm.
func (c *Client) MasterToken() (string, error) {
	userB64, err := kube.Kubectl("-n", c.NS, "get", "secret", "keycloak-initial-admin",
		"-o", "jsonpath={.data.username}")
	if err != nil {
		return "", err
	}
	passB64, err := kube.Kubectl("-n", c.NS, "get", "secret", "keycloak-initial-admin",
		"-o", "jsonpath={.data.password}")
	if err != nil {
		return "", err
	}
	user, err := base64.StdEncoding.DecodeString(strings.TrimSpace(userB64))
	if err != nil {
		return "", fmt.Errorf("decoding keycloak-initial-admin username: %w", err)
	}
	pass, err := base64.StdEncoding.DecodeString(strings.TrimSpace(passB64))
	if err != nil {
		return "", fmt.Errorf("decoding keycloak-initial-admin password: %w", err)
	}
	body, err := kube.Curl(c.NS, c.Toolbox,
		c.base()+"/realms/master/protocol/openid-connect/token",
		"-d", "grant_type=password",
		"-d", "client_id=admin-cli",
		"-d", "username="+string(user),
		"-d", "password="+string(pass),
	)
	if err != nil {
		return "", err
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return "", fmt.Errorf("decoding master token response: %w (%s)", err, body)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("master token response carried no access_token")
	}
	return resp.AccessToken, nil
}

// RealmExists reports whether the given realm exists. curl -sf turns the
// 404 into an error, which maps to false — transient errors also map to
// false so callers can poll (they fail with context when it never flips).
func (c *Client) RealmExists(token, realm string) bool {
	var realmRep struct {
		Realm string `json:"realm"`
	}
	if err := c.get("/admin/realms/"+realm, token, &realmRep); err != nil {
		return false
	}
	return realmRep.Realm == realm
}

// FindClientIn returns the internal id (UUID) of the client with the given
// clientId in the given realm, or "" when absent.
func (c *Client) FindClientIn(token, realm, clientID string) (string, error) {
	var clients []struct {
		ID string `json:"id"`
	}
	if err := c.get("/admin/realms/"+realm+"/clients?clientId="+clientID, token, &clients); err != nil {
		return "", err
	}
	if len(clients) == 0 {
		return "", nil
	}
	return clients[0].ID, nil
}

// UserToken is the dev-admin password grant with the organization scope.
func (c *Client) UserToken() (string, error) {
	return c.PasswordToken("dev-admin", "dev-admin", "openid organization:*")
}

// PasswordToken fetches a direct-grant token for an arbitrary user/scope.
func (c *Client) PasswordToken(username, password, scope string) (string, error) {
	return c.tokenEndpoint(
		"grant_type=password",
		"client_id=inari-server",
		"username="+username,
		"password="+password,
		"scope="+scope,
	)
}

// ClientSecret returns the current secret of the OIDC client with the given
// clientId (e.g. the per-cluster agent client).
func (c *Client) ClientSecret(adminToken, clientID string) (string, error) {
	var clients []struct {
		ID string `json:"id"`
	}
	if err := c.get("/admin/realms/inari/clients?clientId="+clientID, adminToken, &clients); err != nil {
		return "", err
	}
	if len(clients) == 0 {
		return "", fmt.Errorf("keycloak client %q not found", clientID)
	}
	var sec struct {
		Value string `json:"value"`
	}
	if err := c.get("/admin/realms/inari/clients/"+clients[0].ID+"/client-secret", adminToken, &sec); err != nil {
		return "", err
	}
	return sec.Value, nil
}

// ---------------------------------------------------------------------------
// Admin writes (phase 3 of the shell→Go migration: the KC realm/user
// seeding ported from golden-path.sh). Bodies stay literal JSON so the GAP
// shapes remain byte-identical to the script's.
// ---------------------------------------------------------------------------

// post issues an authenticated POST with a JSON body (no response body
// expected; curl -sf surfaces non-2xx as an error).
func (c *Client) post(path, token, jsonBody string) error {
	_, err := kube.Curl(c.NS, c.Toolbox,
		"-X", "POST", "-H", "Authorization: Bearer "+token,
		"-H", "Content-Type: application/json",
		"-d", jsonBody, "-o", "/dev/null", c.base()+path)
	return err
}

// put issues an authenticated PUT with an optional JSON body.
func (c *Client) put(path, token, jsonBody string) error {
	args := []string{"-X", "PUT", "-H", "Authorization: Bearer " + token, "-o", "/dev/null"}
	if jsonBody != "" {
		args = append(args, "-H", "Content-Type: application/json", "-d", jsonBody)
	}
	args = append(args, c.base()+path)
	_, err := kube.Curl(c.NS, c.Toolbox, args...)
	return err
}

// FindUser returns the internal id of the user, or "" when absent.
func (c *Client) FindUser(token, username string) (string, error) {
	var users []struct {
		ID string `json:"id"`
	}
	if err := c.get("/admin/realms/inari/users?username="+username, token, &users); err != nil {
		return "", err
	}
	if len(users) == 0 {
		return "", nil
	}
	return users[0].ID, nil
}

// EnsureUser creates the user when missing and forces the final account
// state either way (KC 26.x: create alone can leave the account unverified,
// and the password grant then fails with "Account is not fully set up").
// createJSON must carry username/credentials; updateJSON the forced state.
func (c *Client) EnsureUser(token, username, createJSON, updateJSON string) (string, error) {
	uid, err := c.FindUser(token, username)
	if err != nil {
		return "", err
	}
	if uid == "" {
		if err := c.post("/admin/realms/inari/users", token, createJSON); err != nil {
			return "", fmt.Errorf("creating user %s: %w", username, err)
		}
		if uid, err = c.FindUser(token, username); err != nil || uid == "" {
			return "", fmt.Errorf("user %s not found after create (id=%q, err=%v)", username, uid, err)
		}
	}
	if err := c.put("/admin/realms/inari/users/"+uid, token, updateJSON); err != nil {
		return "", fmt.Errorf("forcing final state for user %s: %w", username, err)
	}
	return uid, nil
}

// FindClient returns the internal id (UUID) of the client with the given
// clientId, or "" when absent.
func (c *Client) FindClient(token, clientID string) (string, error) {
	var clients []struct {
		ID string `json:"id"`
	}
	if err := c.get("/admin/realms/inari/clients?clientId="+clientID, token, &clients); err != nil {
		return "", err
	}
	if len(clients) == 0 {
		return "", nil
	}
	return clients[0].ID, nil
}

// EnsureClient creates the client when missing and returns its internal id.
func (c *Client) EnsureClient(token, clientID, createJSON string) (string, error) {
	id, err := c.FindClient(token, clientID)
	if err != nil {
		return "", err
	}
	if id == "" {
		if err := c.post("/admin/realms/inari/clients", token, createJSON); err != nil {
			return "", fmt.Errorf("creating client %s: %w", clientID, err)
		}
		if id, err = c.FindClient(token, clientID); err != nil || id == "" {
			return "", fmt.Errorf("client %s not found after create (id=%q, err=%v)", clientID, id, err)
		}
	}
	return id, nil
}

// MapperNames lists the protocol mapper names on a client (internal id).
func (c *Client) MapperNames(token, clientUUID string) ([]string, error) {
	var mappers []struct {
		Name string `json:"name"`
	}
	if err := c.get("/admin/realms/inari/clients/"+clientUUID+"/protocol-mappers/models", token, &mappers); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(mappers))
	for _, m := range mappers {
		names = append(names, m.Name)
	}
	return names, nil
}

// EnsureMapper adds the protocol mapper when absent. createErrs are
// tolerated by the caller's retry loop (the script retried the audience
// mapper three times — Keycloak occasionally 409s a fresh client).
func (c *Client) EnsureMapper(token, clientUUID, name, createJSON string) error {
	names, err := c.MapperNames(token, clientUUID)
	if err != nil {
		return err
	}
	for _, n := range names {
		if n == name {
			return nil
		}
	}
	return c.post("/admin/realms/inari/clients/"+clientUUID+"/protocol-mappers/models", token, createJSON)
}

// EnsureClientScope recreates a built-in client scope the realm import
// skipped (GAP(default-scopes): an explicit clientScopes array in the
// import JSON suppresses Keycloak's built-ins, and every token request dies
// with invalid_scope) and attaches it to the client's default scopes.
// Idempotent.
func (c *Client) EnsureClientScope(token, name, createJSON, clientUUID string) error {
	find := func() (string, error) {
		var scopes []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := c.get("/admin/realms/inari/client-scopes", token, &scopes); err != nil {
			return "", err
		}
		for _, s := range scopes {
			if s.Name == name {
				return s.ID, nil
			}
		}
		return "", nil
	}
	sid, err := find()
	if err != nil {
		return err
	}
	if sid == "" {
		if err := c.post("/admin/realms/inari/client-scopes", token, createJSON); err != nil {
			return fmt.Errorf("creating client scope %s: %w", name, err)
		}
		if sid, err = find(); err != nil || sid == "" {
			return fmt.Errorf("client scope %s not found after create (id=%q, err=%v)", name, sid, err)
		}
	}
	return c.put("/admin/realms/inari/clients/"+clientUUID+"/default-client-scopes/"+sid, token, "")
}

// EnsureGroup creates the top-level group when missing and returns its id.
func (c *Client) EnsureGroup(token, name string) (string, error) {
	find := func() (string, error) {
		var groups []struct {
			ID string `json:"id"`
		}
		if err := c.get("/admin/realms/inari/groups?exact=true&search="+name, token, &groups); err != nil {
			return "", err
		}
		if len(groups) == 0 {
			return "", nil
		}
		return groups[0].ID, nil
	}
	id, err := find()
	if err != nil {
		return "", err
	}
	if id == "" {
		if err := c.post("/admin/realms/inari/groups", token, `{"name":"`+name+`"}`); err != nil {
			return "", fmt.Errorf("creating group %s: %w", name, err)
		}
		if id, err = find(); err != nil || id == "" {
			return "", fmt.Errorf("group %s not found after create (id=%q, err=%v)", name, id, err)
		}
	}
	return id, nil
}

// GroupByPath returns the id of the group at the given full path
// (e.g. tenant-e2e-org/viewers), or "" when absent.
func (c *Client) GroupByPath(token, path string) (string, error) {
	var g struct {
		ID string `json:"id"`
	}
	if err := c.get("/admin/realms/inari/group-by-path/"+path, token, &g); err != nil {
		return "", nil // absent group 404s under curl -sf: treat as empty
	}
	return g.ID, nil
}

// AddUserToGroup joins the user to the group (idempotent 204).
func (c *Client) AddUserToGroup(token, userID, groupID string) error {
	return c.put("/admin/realms/inari/users/"+userID+"/groups/"+groupID, token, "")
}

// Claims decodes the payload of a JWT without verifying it (the suite trusts
// the cluster-internal issuer; it only inspects claims).
func Claims(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT (%d segments)", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decoding JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parsing JWT payload: %w", err)
	}
	return claims, nil
}
