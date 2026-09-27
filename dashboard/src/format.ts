const integer = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });
const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });
const percent = new Intl.NumberFormat(undefined, { style: "percent", maximumFractionDigits: 1 });
const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });
const clock = new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" });

export const fmtInt = (n: number): string => integer.format(n);
export const fmtCompact = (n: number): string => compact.format(n);

/** A ratio as a percentage, or an em dash when the denominator is zero. */
export function fmtRatio(part: number, whole: number): string {
  return whole > 0 ? percent.format(part / whole) : "—";
}

export const fmtPct = (ratio: number): string => percent.format(ratio);

export function fmtMs(ms: number): string {
  if (ms === 0) return "0 ms";
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 100) return `${ms.toFixed(1)} ms`;
  return `${fmtInt(ms)} ms`;
}

export function fmtDate(iso: string | null): string {
  return iso ? dateTime.format(new Date(iso)) : "—";
}

export const fmtClock = (t: number): string => clock.format(new Date(t));

/** Go duration strings as people write them: "1h0m0s" → "1h", "15m0s" → "15m", "168h0m0s" → "7d". */
export function fmtWindow(goDuration: string): string {
  const s = goDuration.replace(/([hm])0s$/, "$1").replace(/h0m$/, "h");
  const hours = /^(\d+)h$/.exec(s);
  if (hours?.[1] && Number(hours[1]) >= 24 && Number(hours[1]) % 24 === 0) return `${Number(hours[1]) / 24}d`;
  return s;
}
