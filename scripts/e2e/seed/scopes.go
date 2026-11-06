// Built-in client-scope + protocol-mapper repair (GAP markers ported from
// scripts/e2e/stack/suite/seed.go — the literal JSON bodies stay
// byte-identical to the Ginkgo suite's; remove a repair when the upstream
// fix lands in the realm import).
//
// Why this exists on the live env (verified 2026-11-05): the realm import
// (charts/inari-platform/files/realm-inari.json) carries an explicit
// clientScopes array, which makes Keycloak skip creating its built-in
// default scopes — the e2e realm has NO basic/organization scopes and both
// inari-server and inari-ui import with defaultClientScopes: []. Every
// token request dies with invalid_scope until this repair runs.
package main

import (
	"encoding/json"
	"fmt"
)

// GAP(default-scopes): recreate the built-in scopes the realm import
// suppressed and attach them to the clients (idempotent).
//
// Without basic, tokens carry no sub claim (KC 26 user-profile model).
const basicScope = `{"name":"basic","protocol":"openid-connect","attributes":{"include.in.token.scope":"false","display.on.consent.screen":"false"},"protocolMappers":[{"name":"sub","protocol":"openid-connect","protocolMapper":"oidc-sub-mapper","config":{"access.token.claim":"true","id.token.claim":"true"}}]}`

// Without organization, scope="openid organization:*" is rejected.
const organizationScope = `{"name":"organization","protocol":"openid-connect","attributes":{"include.in.token.scope":"true","display.on.consent.screen":"false"},"protocolMappers":[{"name":"organization","protocol":"openid-connect","protocolMapper":"oidc-organization-membership-mapper","config":{"id.token.claim":"true","access.token.claim":"true","claim.name":"organization","jsonType.label":"String","multivalued":"true"}}]}`

// GAP(kc-groups-mapper): the groups claim must carry full group paths (the
// server's PlatformGroupSync matches on /platform-admins).
const groupsMapper = `{"name":"groups","protocol":"openid-connect","protocolMapper":"oidc-group-membership-mapper","config":{"claim.name":"groups","full.path":"true","id.token.claim":"true","access.token.claim":"true","userinfo.token.claim":"true"}}`

// ensureRealmScope makes a realm client scope exist (by name) and returns
// its id. Existing scope = no-op (the seed never rewrites a scope body —
// the import or a previous seed run owns it).
func ensureRealmScope(kc *KC, adminToken, name, body string) (id string, created bool, err error) {
	scopes, err := kc.ListClientScopes(adminToken)
	if err != nil {
		return "", false, err
	}
	for _, s := range scopes {
		if s.Name == name {
			return s.ID, false, nil
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		return "", false, err
	}
	id, err = kc.CreateClientScope(adminToken, decoded)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// ensureDefaultScope attaches scope `scopeName` as a default scope of the
// client `clientID`, creating the scope from `scopeBody` when missing.
func ensureDefaultScope(kc *KC, adminToken, clientID, scopeName, scopeBody string) (bool, error) {
	client, err := kc.GetClient(adminToken, clientID)
	if err != nil {
		return false, err
	}
	if client == nil {
		return false, fmt.Errorf("client %q not found (realm import did not run?)", clientID)
	}
	for _, s := range stringSlice(client["defaultClientScopes"]) {
		if s == scopeName {
			return false, nil // already attached
		}
	}
	scopeID, _, err := ensureRealmScope(kc, adminToken, scopeName, scopeBody)
	if err != nil {
		return false, err
	}
	uuid, _ := client["id"].(string)
	if uuid == "" {
		return false, fmt.Errorf("client %q representation has no id", clientID)
	}
	if err := kc.AssignDefaultClientScope(adminToken, uuid, scopeID); err != nil {
		return false, err
	}
	return true, nil
}
