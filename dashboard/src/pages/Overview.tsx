import { useCallback, useEffect, useState } from "react";
import { Bar, BarChart, CartesianGrid, Cell, Legend, Line, LineChart, Tooltip, XAxis, YAxis } from "recharts";
import type { AdminClient } from "../api/client";
import type { Summary } from "../api/types";
import { ChartFrame, axisProps, tooltipProps } from "../components/charts";
import { Async, ErrorState, Loading, Panel, StatCard } from "../components/ui";
import { fmtClock, fmtCompact, fmtInt, fmtMs, fmtRatio, fmtWindow } from "../format";
import { useAsync, useInterval } from "../hooks";
import { usePalette } from "../palette";

const SAMPLE_EVERY_MS = 10_000;
const MAX_SAMPLES = 60; // 10 minutes of history

interface Sample {
  t: number;
  requests: number;
  errors: number;
  rateLimited: number;
  p50: number;
  p95: number;
  p99: number;
}

/**
 * The management API reports window totals, not time series, so the live charts
 * sample the rolling one-minute summary every 10 s while the page is open.
 */
function useLiveSeries(client: AdminClient) {
  const [samples, setSamples] = useState<Sample[]>([]);
  const [error, setError] = useState<Error | undefined>(undefined);

  const sample = useCallback(() => {
    client.summary("1m").then(
      (s) => {
        setError(undefined);
        setSamples((prev) => {
          // Ignore a sample right after the previous one (effect re-runs, manual refresh).
          const last = prev.at(-1);
          if (last && Date.now() - last.t < SAMPLE_EVERY_MS / 2) return prev;
          return [
            ...prev,
            { t: Date.now(), requests: s.requests, errors: s.errors, rateLimited: s.rate_limited, ...s.latency_ms },
          ].slice(-MAX_SAMPLES);
        });
      },
      (e: unknown) => setError(e instanceof Error ? e : new Error(String(e))),
    );
  }, [client]);

  useEffect(sample, [sample]);
  useInterval(sample, SAMPLE_EVERY_MS);
  return { samples, error };
}

export function Overview({ client, window }: { client: AdminClient; window: string }) {
  const summary = useAsync((signal) => client.summary(window, signal), [client, window]);
  useInterval(summary.reload, 30_000);
  const live = useLiveSeries(client);

  return (
    <>
      <Async state={summary}>{(s) => <SummaryCards s={s} />}</Async>
      <div className="grid-2">
        <LiveTraffic samples={live.samples} error={live.error} />
        <LiveLatency samples={live.samples} error={live.error} />
      </div>
      <Async state={summary}>{(s) => <Outcomes s={s} />}</Async>
    </>
  );
}

function SummaryCards({ s }: { s: Summary }) {
  const lost = s.collector.dropped + s.collector.failed;
  return (
    <div className="stats" aria-label={`Totals for the last ${fmtWindow(s.window)}`}>
      <StatCard label="Requests" value={fmtInt(s.requests)} hint={`last ${fmtWindow(s.window)}`} />
      <StatCard label="Error rate (5xx)" value={fmtRatio(s.errors, s.requests)} hint={`${fmtInt(s.errors)} errors`} />
      <StatCard label="Timeouts (504)" value={fmtInt(s.timeouts)} />
      <StatCard label="Rate limited (429)" value={fmtInt(s.rate_limited)} hint={fmtRatio(s.rate_limited, s.requests)} />
      <StatCard
        label="Cache hit ratio"
        value={fmtRatio(s.cache_hits, s.cache_hits + s.cache_misses)}
        hint={`${fmtInt(s.cache_hits)} hits / ${fmtInt(s.cache_misses)} misses`}
      />
      <StatCard label="Latency p50" value={fmtMs(s.latency_ms.p50)} />
      <StatCard label="Latency p95" value={fmtMs(s.latency_ms.p95)} />
      <StatCard label="Latency p99" value={fmtMs(s.latency_ms.p99)} />
      <StatCard
        label="Analytics events lost"
        value={fmtInt(lost)}
        hint={lost > 0 ? `${fmtInt(s.collector.dropped)} dropped, ${fmtInt(s.collector.failed)} failed` : "since gateway start"}
        {...(lost > 0 ? { tone: "warn" as const } : {})}
      />
    </div>
  );
}

function LiveTraffic({ samples, error }: { samples: Sample[]; error: Error | undefined }) {
  const p = usePalette();
  return (
    <Panel title="Live traffic — requests in the last minute">
      <LiveBody samples={samples} error={error}>
        <ChartFrame label="Requests, 5xx errors, and rate-limited requests per rolling minute, sampled every 10 seconds">
          <LineChart data={samples} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={p.grid} vertical={false} />
            <XAxis dataKey="t" type="number" scale="time" domain={["dataMin", "dataMax"]} tickFormatter={fmtClock} {...axisProps(p)} minTickGap={40} />
            <YAxis tickFormatter={fmtCompact} {...axisProps(p)} width={48} allowDecimals={false} />
            <Tooltip labelFormatter={(t) => fmtClock(Number(t))} {...tooltipProps(p)} />
            <Legend itemSorter={null} />
            <Line type="monotone" dataKey="requests" name="Requests" stroke={p.series[0]} strokeWidth={2} dot={false} isAnimationActive={false} />
            <Line type="monotone" dataKey="errors" name="5xx errors" stroke={p.critical} strokeWidth={2} dot={false} isAnimationActive={false} />
            <Line type="monotone" dataKey="rateLimited" name="Rate limited" stroke={p.serious} strokeWidth={2} dot={false} isAnimationActive={false} />
          </LineChart>
        </ChartFrame>
      </LiveBody>
    </Panel>
  );
}

function LiveLatency({ samples, error }: { samples: Sample[]; error: Error | undefined }) {
  const p = usePalette();
  return (
    <Panel title="Live latency — last minute">
      <LiveBody samples={samples} error={error}>
        <ChartFrame label="Latency percentiles p50, p95, and p99 over the rolling last minute, sampled every 10 seconds">
          <LineChart data={samples} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
            <CartesianGrid stroke={p.grid} vertical={false} />
            <XAxis dataKey="t" type="number" scale="time" domain={["dataMin", "dataMax"]} tickFormatter={fmtClock} {...axisProps(p)} minTickGap={40} />
            <YAxis tickFormatter={(v: number) => `${v} ms`} {...axisProps(p)} width={64} />
            <Tooltip labelFormatter={(t) => fmtClock(Number(t))} formatter={(v) => fmtMs(Number(v))} {...tooltipProps(p)} />
            <Legend itemSorter={null} />
            <Line type="monotone" dataKey="p50" name="p50" stroke={p.series[0]} strokeWidth={2} dot={false} isAnimationActive={false} />
            <Line type="monotone" dataKey="p95" name="p95" stroke={p.series[1]} strokeWidth={2} dot={false} isAnimationActive={false} />
            <Line type="monotone" dataKey="p99" name="p99" stroke={p.series[2]} strokeWidth={2} dot={false} isAnimationActive={false} />
          </LineChart>
        </ChartFrame>
      </LiveBody>
    </Panel>
  );
}

function LiveBody({ samples, error, children }: { samples: Sample[]; error: Error | undefined; children: React.ReactNode }) {
  if (error && samples.length === 0) return <ErrorState error={error} />;
  if (samples.length < 2) return <Loading label="Collecting samples (one every 10 s)…" />;
  return (
    <>
      {error && <p className="muted" role="status">Latest sample failed: {error.message}</p>}
      {children}
    </>
  );
}

function Outcomes({ s }: { s: Summary }) {
  const p = usePalette();
  const serverErrors = s.errors - s.timeouts;
  const data = [
    { name: "Other responses", value: Math.max(0, s.requests - s.errors - s.rate_limited), color: p.series[0] },
    { name: "5xx (excl. 504)", value: serverErrors, color: p.critical },
    { name: "504 timeouts", value: s.timeouts, color: p.critical },
    { name: "429 rate limited", value: s.rate_limited, color: p.serious },
  ];
  return (
    <Panel title={`Outcomes — last ${fmtWindow(s.window)}`}>
      {s.requests === 0 ? (
        <div className="empty">No requests in this window.</div>
      ) : (
        <ChartFrame label={`Request outcomes in the last ${fmtWindow(s.window)}`} height={200}>
          <BarChart data={data} layout="vertical" margin={{ top: 4, right: 24, bottom: 4, left: 8 }}>
            <CartesianGrid stroke={p.grid} horizontal={false} />
            <XAxis type="number" tickFormatter={fmtCompact} {...axisProps(p)} allowDecimals={false} />
            <YAxis type="category" dataKey="name" {...axisProps(p)} width={130} />
            <Tooltip formatter={(v) => fmtInt(Number(v))} {...tooltipProps(p)} />
            <Bar dataKey="value" name="Requests" radius={[0, 4, 4, 0]} barSize={20} isAnimationActive={false}>
              {data.map((d) => (
                <Cell key={d.name} fill={d.color} />
              ))}
            </Bar>
          </BarChart>
        </ChartFrame>
      )}
    </Panel>
  );
}
