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

/** A run returned by the spendlease control plane. */
export interface RunRecord {
  id: string;
  principal_id: string;
  parent_run_id?: string;
  budget_usd: string;
  status: "active" | "closed";
  created_at: string;
  closed_at?: string;
}

/** A lease record. The token appears only on the issue response. */
export interface LeaseRecord {
  id: string;
  run_id: string;
  providers: string[];
  ceiling_usd: string;
  expires_at: string;
  revoked_at?: string;
  created_at: string;
  status: "active" | "revoked" | "expired";
  token?: string;
}

/** One budget ceiling in a run's ancestry. */
export interface BudgetLevel {
  run_id: string;
  status: "active" | "closed";
  budget_usd: string;
  spent_usd: string;
  held_usd: string;
  remaining_usd: string;
  unlimited: boolean;
}

/** Effective remaining budget after all ancestors are considered. */
export interface BudgetStatus {
  run_id: string;
  status: "active" | "closed";
  spend_allowed: boolean;
  blocking_run_id?: string;
  unlimited: boolean;
  effective_remaining_usd: string;
  limiting_run_id?: string;
  levels: BudgetLevel[];
}

/** Successful ledger verification result. */
export interface LedgerVerification {
  ok: true;
  entries: number;
  head_hash: string;
  head_sequence?: number;
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

  /** Create a budgeted run for a principal. */
  createRun(principalID: string, budgetUSD: string, parentRunID = ""): Promise<RunRecord> {
    return this.json("POST", `/api/v1/principals/${encodeURIComponent(principalID)}/runs`, {
      budget_usd: budgetUSD,
      parent_run_id: parentRunID,
    });
  }

  /** List a principal's runs, newest first. */
  async listRuns(principalID: string): Promise<RunRecord[]> {
    const result = await this.json<{ runs: RunRecord[] }>(
      "GET",
      `/api/v1/principals/${encodeURIComponent(principalID)}/runs`,
    );
    return result.runs;
  }

  /** Read one run by ID. */
  getRun(runID: string): Promise<RunRecord> {
    return this.json("GET", `/api/v1/runs/${encodeURIComponent(runID)}`);
  }

  /** Close a run so it can no longer issue leases or spend. */
  closeRun(runID: string): Promise<RunRecord> {
    return this.json("POST", `/api/v1/runs/${encodeURIComponent(runID)}/close`, {});
  }

  /** Read effective remaining budget and the limiting ancestor. */
  remainingBudget(runID: string): Promise<BudgetStatus> {
    return this.json("GET", `/api/v1/runs/${encodeURIComponent(runID)}/budget`);
  }

  /** Issue a lease; its token is returned once in this response. */
  issueLease(
    runID: string,
    options: { ttlSeconds?: number; providers?: string[]; ceilingUSD?: string } = {},
  ): Promise<LeaseRecord> {
    return this.json("POST", `/api/v1/runs/${encodeURIComponent(runID)}/leases`, {
      ttl_seconds: options.ttlSeconds ?? 900,
      providers: options.providers ?? [],
      ceiling_usd: options.ceilingUSD ?? "0",
    });
  }

  /** List a run's leases without returning token material. */
  async listLeases(runID: string): Promise<LeaseRecord[]> {
    const result = await this.json<{ leases: LeaseRecord[] }>(
      "GET",
      `/api/v1/runs/${encodeURIComponent(runID)}/leases`,
    );
    return result.leases;
  }

  /** Revoke one lease immediately. */
  revokeLease(leaseID: string): Promise<LeaseRecord> {
    return this.json("POST", `/api/v1/leases/${encodeURIComponent(leaseID)}/revoke`, {});
  }

  /** Verify the complete ledger hash chain. */
  verifyLedger(): Promise<LedgerVerification> {
    return this.json("GET", "/api/v1/ledger/verify");
  }

  /** Export ledger rows as JSON or CSV text. */
  async exportLedger(options: {
    format?: "json" | "csv";
    runID?: string;
    principalID?: string;
    since?: string;
  } = {}): Promise<string> {
    const query = new URLSearchParams();
    query.set("format", options.format ?? "json");
    if (options.runID) query.set("run_id", options.runID);
    if (options.principalID) query.set("principal_id", options.principalID);
    if (options.since) query.set("since", options.since);
    const response = await this.request("GET", `/api/v1/ledger/export?${query.toString()}`);
    return response.text();
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

  private async json<T>(method: "GET" | "POST", path: string, body?: unknown): Promise<T> {
    const response = await this.request(method, path, body);
    return response.json() as Promise<T>;
  }

  private async request(method: "GET" | "POST", path: string, body?: unknown): Promise<Response> {
    const headers: Record<string, string> = {};
    if (body !== undefined) headers["content-type"] = "application/json";
    if (method === "POST") headers["x-spendlease-admin"] = "1";
    if (this.token) headers.authorization = `Bearer ${this.token}`;
    const response = await fetch(this.baseURL + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) {
      const text = await response.text();
      throw new SpendleaseError(response.status, text.trim() || response.statusText);
    }
    return response;
  }
}
