/** Kepler stub: agent color to ink theme mapping. */
import type { TextProps } from "../ink.js";

export function toInkColor(color: string | undefined): TextProps["color"] {
  if (!color) return "text";
  return `ansi:${color}` as TextProps["color"];
}
