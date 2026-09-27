import { useDarkMode } from "./hooks";

// Reference data-viz palette (validated for CVD separation and contrast in
// both modes). Categorical slots are assigned in fixed order, never cycled.
// Recharts writes colors into SVG attributes, where CSS variables are not
// reliable, so the mode's hex values are chosen here.
export interface Palette {
  series: readonly [string, string, string, string];
  critical: string;
  serious: string;
  good: string;
  grid: string;
  axis: string;
  surface: string;
  text: string;
}

const light: Palette = {
  series: ["#2a78d6", "#eb6834", "#1baf7a", "#eda100"],
  critical: "#d03b3b",
  serious: "#ec835a",
  good: "#0ca30c",
  grid: "#e1e0d9",
  axis: "#898781",
  surface: "#fcfcfb",
  text: "#0b0b0b",
};

const dark: Palette = {
  series: ["#3987e5", "#d95926", "#199e70", "#c98500"],
  critical: "#d03b3b",
  serious: "#ec835a",
  good: "#0ca30c",
  grid: "#2c2c2a",
  axis: "#898781",
  surface: "#1a1a19",
  text: "#ffffff",
};

export function usePalette(): Palette {
  return useDarkMode() ? dark : light;
}
