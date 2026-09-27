import { Bar, BarChart, CartesianGrid, ReferenceLine, Tooltip, XAxis, YAxis } from "recharts";
import type { AdminClient } from "../api/client";
import type { ApiKeyStats, ApplicationStats } from "../api/types";
import { SortableTable, type Column } from "../components/SortableTable";
import { ChartFrame, axisProps, tooltipProps } from "../components/charts";
import { Async, Panel } from "../components/ui";
import { fmtCompact, fmtInt, fmtPct, fmtRatio, fmtWindow } from "../format";
import { useAsync } from "../hooks";
import { usePalette } from "../palette";

const TOP_N = 10;

export function Accounts({ client, window }: { client: AdminClient; window: string }) {
  const accounts = useAsync((signal) => client.accountAnalytics(window, signal), [client, window]);

  return (
    <Async state={accounts}>
      {(a) => {
        const appName = new Map(a.applications.map((app) => [app.application_id, app.application ?? `#${app.application_id}`]));
        return (
          <>
            <div className="grid-2">
              <TopApplications apps={a.applications} window={a.window} />
              <Utilization keys={a.api_keys} appName={appName} window={a.window} />
            </div>
            <Panel title={`Applications — last ${fmtWindow(a.window)}`}>
              <ApplicationTable apps={a.applications} />
            </Panel>
            <Panel title={`API keys — last ${fmtWindow(a.window)}`}>
              <KeyTable keys={a.api_keys} appName={appName} />
            </Panel>
          </>
        );
      }}
    </Async>
  );
}

function ApplicationTable({ apps }: { apps: ApplicationStats[] }) {
  const columns: Column<ApplicationStats>[] = [
    { key: "app", header: "Application", sortValue: (a) => a.application, render: (a) => a.application ?? <span className="muted">deleted (#{a.application_id})</span> },
    { key: "owner", header: "Owner", sortValue: (a) => a.owner_email, render: (a) => <span className="break">{a.owner_email ?? "—"}</span> },
    { key: "plan", header: "Plan", sortValue: (a) => a.plan, render: (a) => a.plan ?? "—" },
    { key: "requests", header: "Requests", align: "right", sortValue: (a) => a.requests, render: (a) => fmtInt(a.requests) },
    { key: "errors", header: "5xx rate", align: "right", sortValue: (a) => (a.requests > 0 ? a.errors / a.requests : null), render: (a) => fmtRatio(a.errors, a.requests) },
    { key: "limited", header: "Rate limited", align: "right", sortValue: (a) => a.rate_limited, render: (a) => fmtInt(a.rate_limited) },
  ];
  return <SortableTable caption="Requests by application" columns={columns} rows={apps} rowKey={(a) => a.application_id} initialSort={{ key: "requests", dir: "desc" }} empty="No authenticated traffic in this window." />;
}

function KeyTable({ keys, appName }: { keys: ApiKeyStats[]; appName: Map<number, string> }) {
  const columns: Column<ApiKeyStats>[] = [
    { key: "id", header: "Key ID", align: "right", sortValue: (k) => k.api_key_id, render: (k) => k.api_key_id },
    { key: "app", header: "Application", sortValue: (k) => appName.get(k.application_id) ?? null, render: (k) => appName.get(k.application_id) ?? `#${k.application_id}` },
    { key: "requests", header: "Requests", align: "right", sortValue: (k) => k.requests, render: (k) => fmtInt(k.requests) },
    { key: "limited", header: "Rate limited", align: "right", sortValue: (k) => k.rate_limited, render: (k) => fmtInt(k.rate_limited) },
    { key: "peak", header: "Peak / min", align: "right", sortValue: (k) => k.peak_requests_per_minute, render: (k) => fmtInt(k.peak_requests_per_minute) },
    { key: "limit", header: "Limit / min", align: "right", sortValue: (k) => k.limit_requests_per_minute, render: (k) => (k.limit_requests_per_minute === null ? "—" : fmtInt(k.limit_requests_per_minute)) },
    { key: "util", header: "Plan utilization", sortValue: (k) => k.plan_utilization, render: (k) => <UtilizationBar value={k.plan_utilization} /> },
  ];
  return <SortableTable caption="Requests and plan utilization by API key" columns={columns} rows={keys} rowKey={(k) => k.api_key_id} initialSort={{ key: "util", dir: "desc" }} empty="No authenticated traffic in this window." />;
}

/** A meter for peak-minute usage vs the plan limit; over 100% means demand exceeded the plan. */
function UtilizationBar({ value }: { value: number | null }) {
  if (value === null) return <span className="muted">—</span>;
  const over = value > 1;
  return (
    <span className="meter-cell">
      <span className="meter" aria-hidden="true">
        <span className={over ? "meter-fill over" : "meter-fill"} style={{ width: `${Math.min(100, value * 100)}%` }} />
      </span>
      <span>
        {fmtPct(value)}
        {over && <span className="over-label"> ⚠ over limit</span>}
      </span>
    </span>
  );
}

function TopApplications({ apps, window }: { apps: ApplicationStats[]; window: string }) {
  const p = usePalette();
  const data = [...apps]
    .sort((a, b) => b.requests - a.requests)
    .slice(0, TOP_N)
    .map((a) => ({ name: a.application ?? `#${a.application_id}`, requests: a.requests }));
  return (
    <Panel title={`Top applications by requests — last ${fmtWindow(window)}`}>
      {data.length === 0 ? (
        <div className="empty">No authenticated traffic in this window.</div>
      ) : (
        <ChartFrame label={`Top ${data.length} applications by requests in the last ${fmtWindow(window)}`}>
          <BarChart data={data} layout="vertical" margin={{ top: 4, right: 24, bottom: 4, left: 8 }}>
            <CartesianGrid stroke={p.grid} horizontal={false} />
            <XAxis type="number" tickFormatter={fmtCompact} {...axisProps(p)} allowDecimals={false} />
            <YAxis type="category" dataKey="name" {...axisProps(p)} width={120} />
            <Tooltip formatter={(v) => fmtInt(Number(v))} {...tooltipProps(p)} />
            <Bar dataKey="requests" name="Requests" fill={p.series[0]} radius={[0, 4, 4, 0]} maxBarSize={22} isAnimationActive={false} />
          </BarChart>
        </ChartFrame>
      )}
    </Panel>
  );
}

function Utilization({ keys, appName, window }: { keys: ApiKeyStats[]; appName: Map<number, string>; window: string }) {
  const p = usePalette();
  const data = keys
    .filter((k) => k.plan_utilization !== null)
    .sort((a, b) => (b.plan_utilization ?? 0) - (a.plan_utilization ?? 0))
    .slice(0, TOP_N)
    .map((k) => ({ name: `Key ${k.api_key_id} · ${appName.get(k.application_id) ?? ""}`, pct: (k.plan_utilization ?? 0) * 100 }));
  return (
    <Panel title={`Plan utilization (peak minute ÷ limit) — last ${fmtWindow(window)}`}>
      {data.length === 0 ? (
        <div className="empty">No authenticated traffic in this window.</div>
      ) : (
        <ChartFrame label={`Top ${data.length} API keys by plan utilization in the last ${fmtWindow(window)}; the line marks 100% of the plan limit`}>
          <BarChart data={data} layout="vertical" margin={{ top: 4, right: 24, bottom: 4, left: 8 }}>
            <CartesianGrid stroke={p.grid} horizontal={false} />
            <XAxis type="number" tickFormatter={(v: number) => `${v}%`} {...axisProps(p)} domain={[0, (max: number) => Math.max(100, Math.ceil(max / 10) * 10)]} />
            <YAxis type="category" dataKey="name" {...axisProps(p)} width={140} />
            <Tooltip formatter={(v) => `${Number(v).toFixed(1)}%`} {...tooltipProps(p)} />
            <ReferenceLine x={100} stroke={p.critical} strokeDasharray="4 4" label={{ value: "limit", fill: p.axis, fontSize: 12, position: "top" }} />
            <Bar dataKey="pct" name="Utilization" fill={p.series[0]} radius={[0, 4, 4, 0]} maxBarSize={22} isAnimationActive={false} />
          </BarChart>
        </ChartFrame>
      )}
    </Panel>
  );
}
