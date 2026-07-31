declare const process: { env: Record<string, string | undefined> };

/** Configuration for a vendor SDK using a spendlease lease. */
export class Lease {
  /** The short-lived `sll_` token. */
  readonly token: string;
  /** Root URL of the spendlease gateway. */
  readonly baseURL: string;

  /** Create a lease configuration. */
  constructor(token: string, baseURL = "http://localhost:4000") {
    if (!token.startsWith("sll_")) {
      throw new TypeError("token must be a spendlease lease token beginning with sll_");
    }
    this.token = token;
    this.baseURL = baseURL.replace(/\/$/, "");
  }

  /** Read SPENDLEASE_LEASE_TOKEN and SPENDLEASE_URL from the process environment. */
  static fromEnv(env: Record<string, string | undefined> = process.env): Lease {
    const token = env.SPENDLEASE_LEASE_TOKEN;
    if (!token) throw new TypeError("SPENDLEASE_LEASE_TOKEN is not set");
    return new Lease(token, env.SPENDLEASE_URL ?? "http://localhost:4000");
  }

  /** Return options accepted by the official OpenAI JavaScript client. */
  openAIOptions(): { baseURL: string; apiKey: string } {
    return { baseURL: `${this.baseURL}/v1`, apiKey: this.token };
  }

  /** Return options accepted by the official Anthropic JavaScript client. */
  anthropicOptions(): { baseURL: string; apiKey: string } {
    return { baseURL: this.baseURL, apiKey: this.token };
  }
}

/** An unsuccessful response from a spendlease admin endpoint. */
export class SpendleaseError extends Error {
  /** HTTP status returned by spendlease. */
  readonly status: number;

  /** Create an HTTP error. */
  constructor(status: number, message: string) {
    super(`spendlease returned HTTP ${status}: ${message}`);
    this.name = "SpendleaseError";
    this.status = status;
  }
}

/** Minimal client for the guarded spendlease admin endpoints. */
export class AdminClient {
  /** Root URL of the spendlease gateway. */
  readonly baseURL: string;
  /** Optional off-machine admin token. */
  readonly token?: string;

  /** Create an admin client. */
  constructor(baseURL = "http://localhost:4000", token?: string) {
    this.baseURL = baseURL.replace(/\/$/, "");
    this.token = token;
  }

  /** Switch a principal between observe and enforce mode. */
  setMode(principalID: string, mode: "observe" | "enforce"): Promise<string> {
    return this.post(`/admin/principals/${encodeURIComponent(principalID)}/mode`, { mode });
  }

  /** Activate the principal-wide lease kill switch. */
  revoke(principalID: string): Promise<string> {
    return this.post(`/admin/principals/${encodeURIComponent(principalID)}/revoke`, {});
  }

  private async post(path: string, fields: Record<string, string>): Promise<string> {
    const headers: Record<string, string> = {
      "content-type": "application/x-www-form-urlencoded",
      "x-spendlease-admin": "1",
    };
    if (this.token) headers.authorization = `Bearer ${this.token}`;
    const response = await fetch(this.baseURL + path, {
      method: "POST",
      headers,
      body: new URLSearchParams(fields),
    });
    const body = await response.text();
    if (!response.ok) throw new SpendleaseError(response.status, body.trim() || response.statusText);
    return body;
  }
}
