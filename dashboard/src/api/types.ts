// Response shapes of the gateway management API (internal/admin, internal/metrics).
// Timestamps are RFC 3339 strings; nullable fields mirror Go pointers.

export type KeyStatus = "active" | "revoked";

export interface ApiKey {
  id: number;
  status: KeyStatus;
  application_id: number;
  application: string;
  owner_email: string;
  plan: string;
  created_at: string;
  revoked_at: string | null;
}

export interface ApiKeyList {
  api_keys: ApiKey[];
  next_after: number | null;
}

export interface CreateApiKeyRequest {
  email: string;
  application: string;
  plan?: string;
}

export interface CreatedApiKey {
  api_key: string;
  note: string;
  key: ApiKey;
}

export interface Plan {
  id: number;
  name: string;
  requests_per_minute: number;
  applications: number;
  created_at: string;
}

export interface Route {
  id: number;
  prefix: string;
  upstream: string;
  cache_ttl: string;
  cache_ttl_ms: number;
  created_at: string;
  updated_at: string;
}

export interface RoutePatch {
  upstream?: string;
  cache_ttl?: string;
}

export interface Latency {
  p50: number;
  p95: number;
  p99: number;
}

export interface Traffic {
  requests: number;
  errors: number;
  timeouts: number;
  rate_limited: number;
  cache_hits: number;
  cache_misses: number;
  latency_ms: Latency;
}

export interface Window {
  window: string;
  from: string;
  to: string;
}

export interface CollectorStats {
  written: number;
  failed: number;
  dropped: number;
}

export interface Summary extends Window, Traffic {
  collector: CollectorStats;
}

export interface RouteTraffic extends Traffic {
  route: string | null; // null: requests that matched no route
}

export interface RouteAnalytics extends Window {
  routes: RouteTraffic[];
}

export interface ApplicationStats {
  application_id: number;
  application: string | null;
  owner_email: string | null;
  plan: string | null;
  requests: number;
  errors: number;
  rate_limited: number;
}

export interface ApiKeyStats {
  api_key_id: number;
  application_id: number;
  requests: number;
  errors: number;
  rate_limited: number;
  peak_requests_per_minute: number;
  limit_requests_per_minute: number | null;
  plan_utilization: number | null;
}

export interface AccountAnalytics extends Window {
  applications: ApplicationStats[];
  api_keys: ApiKeyStats[];
}

export interface ErrorBody {
  error: string;
  code?: string;
  fields?: Record<string, string>;
  request_id?: string;
}
