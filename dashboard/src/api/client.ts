import type {
  AccountAnalytics,
  ApiKey,
  ApiKeyList,
  CreateApiKeyRequest,
  CreatedApiKey,
  ErrorBody,
  KeyStatus,
  Plan,
  Route,
  RouteAnalytics,
  RoutePatch,
  Summary,
} from "./types";

/** A failed API call, carrying the gateway's structured error body. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
    readonly requestId?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }

  get isAuth(): boolean {
    return this.status === 401 || this.status === 403;
  }
}

export interface KeyQuery {
  status?: KeyStatus;
  applicationId?: number;
  after?: number;
  limit?: number;
}

/** Typed client for the gateway management API. Paths are relative; see vite.config.ts. */
export class AdminClient {
  constructor(
    private readonly token: string,
    private readonly onAuthError: () => void = () => {},
  ) {}

  // ---- API keys ----
  listApiKeys(q: KeyQuery = {}, signal?: AbortSignal): Promise<ApiKeyList> {
    return this.get("/admin/api-keys", {
      status: q.status,
      application_id: q.applicationId,
      after: q.after,
      limit: q.limit,
    }, signal);
  }

  createApiKey(req: CreateApiKeyRequest): Promise<CreatedApiKey> {
    return this.send("POST", "/admin/api-keys", req);
  }

  revokeApiKey(id: number): Promise<ApiKey> {
    return this.send("POST", `/admin/api-keys/${id}/revoke`);
  }

  // ---- plans ----
  async listPlans(signal?: AbortSignal): Promise<Plan[]> {
    return (await this.get<{ plans: Plan[] }>("/admin/plans", {}, signal)).plans;
  }

  updatePlan(id: number, requestsPerMinute: number): Promise<Plan> {
    return this.send("PATCH", `/admin/plans/${id}`, { requests_per_minute: requestsPerMinute });
  }

  // ---- routes ----
  async listRoutes(signal?: AbortSignal): Promise<Route[]> {
    return (await this.get<{ routes: Route[] }>("/admin/routes", {}, signal)).routes;
  }

  updateRoute(id: number, patch: RoutePatch): Promise<Route> {
    return this.send("PATCH", `/admin/routes/${id}`, patch);
  }

  // ---- analytics ----
  summary(window: string, signal?: AbortSignal): Promise<Summary> {
    return this.get("/admin/analytics/summary", { window }, signal);
  }

  routeAnalytics(window: string, signal?: AbortSignal): Promise<RouteAnalytics> {
    return this.get("/admin/analytics/routes", { window }, signal);
  }

  accountAnalytics(window: string, signal?: AbortSignal): Promise<AccountAnalytics> {
    return this.get("/admin/analytics/accounts", { window }, signal);
  }

  // ---- transport ----
  private get<T>(path: string, params: Record<string, string | number | undefined>, signal?: AbortSignal): Promise<T> {
    const qs = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") qs.set(k, String(v));
    }
    const query = qs.toString();
    return this.request<T>("GET", query ? `${path}?${query}` : path, undefined, signal);
  }

  private send<T>(method: "POST" | "PATCH", path: string, body?: unknown): Promise<T> {
    return this.request<T>(method, path, body);
  }

  private async request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
    const headers: Record<string, string> = { Authorization: `Bearer ${this.token}`, Accept: "application/json" };
    const init: RequestInit = { method, headers, cache: "no-store" };
    if (body !== undefined) {
      headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    if (signal) init.signal = signal;

    let res: Response;
    try {
      res = await fetch(path, init);
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") throw e;
      throw new ApiError(0, "network_error", "Could not reach the gateway. Is it running?");
    }

    const text = await res.text();
    let data: unknown = undefined;
    try {
      data = text ? JSON.parse(text) : undefined;
    } catch {
      // Non-JSON (e.g. a proxy error page): reported below.
    }

    if (!res.ok) {
      const err = (data ?? {}) as Partial<ErrorBody>;
      const apiErr = new ApiError(
        res.status,
        err.code ?? `http_${res.status}`,
        err.error ?? `Request failed with status ${res.status}`,
        err.fields ?? {},
        err.request_id,
      );
      if (apiErr.isAuth) this.onAuthError();
      throw apiErr;
    }
    if (data === undefined) {
      throw new ApiError(res.status, "invalid_response", "The gateway returned an empty or non-JSON response.");
    }
    return data as T;
  }
}
