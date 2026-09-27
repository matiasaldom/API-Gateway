import type { ReactNode } from "react";
import { ResponsiveContainer } from "recharts";
import type { Palette } from "../palette";

/** Fixed-height, full-width chart frame with an accessible description. */
export function ChartFrame({ label, height = 260, children }: { label: string; height?: number; children: ReactNode }) {
  return (
    <figure className="chart" role="img" aria-label={label}>
      <ResponsiveContainer width="100%" height={height}>
        {children as React.ReactElement}
      </ResponsiveContainer>
    </figure>
  );
}

// Shared recessive chrome: hairline grid, muted axes, surface-colored tooltip.
export function axisProps(p: Palette) {
  return {
    stroke: p.grid,
    tick: { fill: p.axis, fontSize: 12 },
    tickLine: false,
  } as const;
}

export function tooltipProps(p: Palette) {
  return {
    contentStyle: { background: p.surface, border: `1px solid ${p.grid}`, borderRadius: 8, color: p.text, fontSize: 13 },
    labelStyle: { color: p.text, fontWeight: 600 },
    cursor: { fill: p.grid, opacity: 0.4 },
  } as const;
}
