import { useState, type FormEvent } from "react";
import { Bar, BarChart, CartesianGrid, Legend, Tooltip, XAxis, YAxis } from "recharts";
import type { AdminClient } from "../api/client";
import type { Route, RoutePatch, RouteTraffic } from "../api/types";
import { SortableTable, type Column } from "../components/SortableTable";
import { ChartFrame, axisProps, tooltipProps } from "../components/charts";
import { Async, Dialog, FieldError, FormError, Panel } from "../components/ui";
import { fmtCompact, fmtDate, fmtInt, fmtMs, fmtRatio, fmtWindow } from "../format";
import { useAsync } from "../hooks";
import { usePalette } from "../palette";

interface Row {
  route: Route;
  traffic: RouteTraffic | undefined;
}

export function Routes({ client, window }: { client: AdminClient; window: string }) {
  const routes = useAsync((signal) => client.listRoutes(signal), [client]);
  const traffic = useAsync((signal) => client.routeAnalytics(window, signal), [client, window]);
  const [editing, setEditing] = useState<Route | null>(null);

  const trafficByPrefix = new Map((traffic.data?.routes ?? []).map((t) => [t.route, t]));
  const unmatched = trafficByPrefix.get(null);

  return (
    <>
      <Panel title="Routes" actions={<span className="muted">Prefixes come from gateway.yaml; upstream and cache TTL are editable.</span>}>
        <Async state={routes}>
          {(list) => (
            <RouteTable
              rows={list.map((route) => ({ route, traffic: trafficByPrefix.get(route.prefix) }))}
              window={window}
              onEdit={setEditing}
            />
          )}
        </Async>
        {unmatched && (
          <p className="muted">
            {fmtInt(unmatched.requests)} requests in the last {fmtWindow(window)} matched no route (404).
          </p>
        )}
      </Panel>

      <Async state={traffic}>
        {(t) => (
          <div className="grid-2">
            <RequestsByRoute routes={t.routes} window={t.window} />
            <LatencyByRoute routes={t.routes} window={t.window} />
          </div>
        )}
      </Async>

      <Dialog open={editing !== null} title={`Edit route ${editing?.prefix ?? ""}`} onClose={() => setEditing(null)}>
        {editing && (
          <EditRouteForm
            client={client}
            route={editing}
            onDone={() => {
              setEditing(null);
              routes.reload();
            }}
          />
        )}
      </Dialog>
    </>
  );
}

function RouteTable({ rows, window, onEdit }: { rows: Row[]; window: string; onEdit: (r: Route) => void }) {
  const columns: Column<Row>[] = [
    { key: "prefix", header: "Prefix", sortValue: (r) => r.route.prefix, render: (r) => <code>{r.route.prefix}</code> },
    { key: "upstream", header: "Upstream", sortValue: (r) => r.route.upstream, render: (r) => <span className="break">{r.route.upstream}</span> },
    { key: "ttl", header: "Cache TTL", align: "right", sortValue: (r) => r.route.cache_ttl_ms, render: (r) => (r.route.cache_ttl_ms === 0 ? <span className="muted">off</span> : fmtWindow(r.route.cache_ttl)) },
    { key: "requests", header: `Requests (${fmtWindow(window)})`, align: "right", sortValue: (r) => r.traffic?.requests ?? 0, render: (r) => fmtInt(r.traffic?.requests ?? 0) },
    { key: "errors", header: "5xx rate", align: "right", sortValue: (r) => (r.traffic && r.traffic.requests > 0 ? r.traffic.errors / r.traffic.requests : null), render: (r) => fmtRatio(r.traffic?.errors ?? 0, r.traffic?.requests ?? 0) },
    { key: "p95", header: "p95", align: "right", sortValue: (r) => r.traffic?.latency_ms.p95 ?? null, render: (r) => (r.traffic ? fmtMs(r.traffic.latency_ms.p95) : "—") },
    { key: "hit", header: "Cache hits", align: "right", sortValue: (r) => (r.traffic ? r.traffic.cache_hits : null), render: (r) => fmtRatio(r.traffic?.cache_hits ?? 0, (r.traffic?.cache_hits ?? 0) + (r.traffic?.cache_misses ?? 0)) },
    { key: "updated", header: "Updated", sortValue: (r) => r.route.updated_at, render: (r) => fmtDate(r.route.updated_at) },
    {
      key: "actions",
      header: "",
      render: (r) => (
        <button type="button" className="small" onClick={() => onEdit(r.route)}>
          Edit
        </button>
      ),
    },
  ];
  return <SortableTable caption="Routes" columns={columns} rows={rows} rowKey={(r) => r.route.id} initialSort={{ key: "prefix", dir: "asc" }} empty="No routes configured." />;
}

function EditRouteForm({ client, route, onDone }: { client: AdminClient; route: Route; onDone: () => void }) {
  const [upstream, setUpstream] = useState(route.upstream);
  const [ttl, setTtl] = useState(fmtWindow(route.cache_ttl));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<unknown>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    const patch: RoutePatch = {};
    if (upstream.trim() !== route.upstream) patch.upstream = upstream.trim();
    if (ttl.trim() !== fmtWindow(route.cache_ttl)) patch.cache_ttl = ttl.trim();
    if (Object.keys(patch).length === 0) return onDone();
    setSaving(true);
    setError(null);
    try {
      await client.updateRoute(route.id, patch);
      onDone();
    } catch (err) {
      setError(err);
    } finally {
      setSaving(false);
    }
  }

  return (
    <form onSubmit={submit} className="form">
      <label>
        Upstream URL
        <input type="url" required value={upstream} onChange={(e) => setUpstream(e.target.value)} placeholder="http://service:8080" />
        <FieldError error={error} field="upstream" />
      </label>
      <label>
        Cache TTL
        <input required value={ttl} onChange={(e) => setTtl(e.target.value)} placeholder="0s, 30s, 5m" pattern="^[0-9.]+(ns|us|µs|ms|s|m|h)([0-9.]+(ns|us|µs|ms|s|m|h))*$" title="A duration such as 0s, 500ms, 30s, or 5m" />
        <span className="hint">0s disables caching. Maximum 24h.</span>
        <FieldError error={error} field="cache_ttl" />
      </label>
      <p className="hint">Takes effect on the next request to this gateway instance.</p>
      <FormError error={error} />
      <div className="form-actions">
        <button type="button" className="ghost" onClick={onDone}>
          Cancel
        </button>
        <button type="submit" disabled={saving}>
          {saving ? "Saving…" : "Save"}
        </button>
      </div>
    </form>
  );
}

const label = (r: RouteTraffic) => r.route ?? "(unmatched)";

function RequestsByRoute({ routes, window }: { routes: RouteTraffic[]; window: string }) {
  const p = usePalette();
  const data = routes.map((r) => ({
    name: label(r),
    ok: Math.max(0, r.requests - r.errors - r.rate_limited),
    errors: r.errors,
    rateLimited: r.rate_limited,
  }));
  return (
    <Panel title={`Traffic by route — last ${fmtWindow(window)}`}>
      {data.length === 0 ? (
        <div className="empty">No traffic in this window.</div>
      ) : (
        <ChartFrame label={`Requests per route in the last ${fmtWindow(window)}, split into other responses, 5xx errors, and rate-limited`}>
          <BarChart data={data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={p.grid} vertical={false} />
            <XAxis dataKey="name" {...axisProps(p)} />
            <YAxis tickFormatter={fmtCompact} {...axisProps(p)} width={48} allowDecimals={false} />
            <Tooltip formatter={(v) => fmtInt(Number(v))} {...tooltipProps(p)} />
            <Legend itemSorter={null} />
            <Bar dataKey="ok" name="Other responses" stackId="r" fill={p.series[0]} stroke={p.surface} strokeWidth={2} maxBarSize={48} isAnimationActive={false} />
            <Bar dataKey="errors" name="5xx errors" stackId="r" fill={p.critical} stroke={p.surface} strokeWidth={2} maxBarSize={48} isAnimationActive={false} />
            <Bar dataKey="rateLimited" name="Rate limited" stackId="r" fill={p.serious} stroke={p.surface} strokeWidth={2} radius={[4, 4, 0, 0]} maxBarSize={48} isAnimationActive={false} />
          </BarChart>
        </ChartFrame>
      )}
    </Panel>
  );
}

function LatencyByRoute({ routes, window }: { routes: RouteTraffic[]; window: string }) {
  const p = usePalette();
  const data = routes.map((r) => ({ name: label(r), ...r.latency_ms }));
  return (
    <Panel title={`Latency by route — last ${fmtWindow(window)}`}>
      {data.length === 0 ? (
        <div className="empty">No traffic in this window.</div>
      ) : (
        <ChartFrame label={`Latency percentiles per route in the last ${fmtWindow(window)}`}>
          <BarChart data={data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={p.grid} vertical={false} />
            <XAxis dataKey="name" {...axisProps(p)} />
            <YAxis tickFormatter={(v: number) => `${v} ms`} {...axisProps(p)} width={64} />
            <Tooltip formatter={(v) => fmtMs(Number(v))} {...tooltipProps(p)} />
            <Legend itemSorter={null} />
            <Bar dataKey="p50" name="p50" fill={p.series[0]} radius={[4, 4, 0, 0]} maxBarSize={28} isAnimationActive={false} />
            <Bar dataKey="p95" name="p95" fill={p.series[1]} radius={[4, 4, 0, 0]} maxBarSize={28} isAnimationActive={false} />
            <Bar dataKey="p99" name="p99" fill={p.series[2]} radius={[4, 4, 0, 0]} maxBarSize={28} isAnimationActive={false} />
          </BarChart>
        </ChartFrame>
      )}
    </Panel>
  );
}
