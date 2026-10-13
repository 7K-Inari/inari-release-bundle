// Thin inari-server REST client for setup/assertion READS.
//
// Authenticates with a direct-grant token via the public `inari-server`
// Keycloak client (directAccessGrantsEnabled + audience mapper are
// provisioned by the golden-path stack suite — same token the golden path itself uses).
// Never use this client to mutate what a spec is supposed to exercise
// through the UI; DELETE exists only for spec-owned cleanup of objects the
// spec itself created (see specs/rbac/role-lifecycle.spec.ts).

import { API_BASE, KC_REALM, KC_URL } from "./env";
import { PERSONAS } from "../fixtures/auth";

interface TokenResponse {
  access_token: string;
  expires_in: number;
}

/** Direct-grant token for a persona (default dev-admin). */
export async function getToken(
  username = PERSONAS.devAdmin.username,
  password = PERSONAS.devAdmin.password,
): Promise<string> {
  const res = await fetch(
    `${KC_URL}/realms/${KC_REALM}/protocol/openid-connect/token`,
    {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: new URLSearchParams({
        grant_type: "password",
        client_id: "inari-server",
        username,
        password,
        scope: "openid organization:*",
      }),
    },
  );
  if (!res.ok) {
    throw new Error(`api: token request failed: ${res.status} ${await res.text()}`);
  }
  return ((await res.json()) as TokenResponse).access_token;
}

/** Minimal authenticated REST client against the inari-server API. */
export class ApiClient {
  private constructor(
    readonly base: string,
    private readonly token: string,
  ) {}

  static async forPersona(username?: string, password?: string): Promise<ApiClient> {
    return new ApiClient(API_BASE, await getToken(username, password));
  }

  private async request<T>(method: string, path: string, body?: unknown): Promise<T> {
    // Specs mix both conventions: some pass "/tenants/..." (base carries
    // the /api/v1 prefix), others the full "/api/v1/tenants/...". Accept
    // both — strip the prefix when the base already carries it, or the
    // joined URL 404s (/api/v1/api/v1/...).
    const normalized =
      this.base.endsWith("/api/v1") && path.startsWith("/api/v1/")
        ? path.slice("/api/v1".length)
        : path;
    const res = await fetch(`${this.base}${normalized}`, {
      method,
      headers: {
        authorization: `Bearer ${this.token}`,
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    if (!res.ok) {
      throw new Error(`api: ${method} ${path} failed: ${res.status} ${await res.text()}`);
    }
    const text = await res.text();
    return (text ? JSON.parse(text) : undefined) as T;
  }

  get<T>(path: string): Promise<T> {
    return this.request<T>("GET", path);
  }

  /** Cleanup-only: delete an object the calling spec itself created. */
  async delete(path: string): Promise<void> {
    await this.request<unknown>("DELETE", path);
  }

  /**
   * Setup-only: create a spec-owned object (e.g. the org created by
   * specs/tenants/switching.spec.ts). Never use it to mutate what a spec is
   * supposed to exercise through the UI.
   */
  post<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>("POST", path, body);
  }

  /**
   * Negative-path escape hatch: send a request (optionally with a JSON body)
   * and return the raw status + parsed body WITHOUT throwing on non-2xx.
   * Reserved for specs whose whole point is asserting a rejection (e.g. the
   * 409 guardrails in specs/rbac/) — never use it to mutate state a spec is
   * supposed to exercise through the UI, and never for setup.
   */
  async raw(
    method: string,
    path: string,
    body?: unknown,
  ): Promise<{ status: number; body: unknown }> {
    const res = await fetch(`${this.base}${path}`, {
      method,
      headers: {
        authorization: `Bearer ${this.token}`,
        ...(body === undefined ? {} : { "content-type": "application/json" }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const text = await res.text();
    let parsed: unknown = null;
    try {
      parsed = JSON.parse(text);
    } catch {
      parsed = text;
    }
    return { status: res.status, body: parsed };
  }

  /** Resolve an org's slug from its display name (e.g. "E2E Org" → "e2e-org"). */
  async resolveOrgSlug(displayName: string): Promise<string> {
    const body = await this.get<{ tenants?: { slug: string; displayName: string }[] | null }>(
      "/api/v1/tenants",
    );
    const match = (body.tenants ?? []).find((t) => t.displayName === displayName);
    if (!match) {
      throw new Error(`api: no tenant with displayName "${displayName}"`);
    }
    return match.slug;
  }
}
