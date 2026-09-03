import type { Tool } from "../Tool.js";
import { toolDisplayName, summarizeToolArgs } from "../../lib/toolDisplay.js";

const passthroughSchema = {
  safeParse: (v: unknown) => ({
    success: true as const,
    data: (v && typeof v === "object" ? v : {}) as Record<string, unknown>,
  }),
} as Tool["inputSchema"];

const cache = new Map<string, Tool>();

/** CC AssistantToolUseMessage returns null when findToolByName misses — Kepler registers on demand. */
export function getKeplerToolFallback(name: string): Tool {
  const cached = cache.get(name);
  if (cached) {
    return cached;
  }
  const tool: Tool = {
    name,
    inputSchema: passthroughSchema,
    userFacingName: () => toolDisplayName(name),
    // Must be non-null — AssistantToolUseMessage bails when renderToolUseMessage is null.
    renderToolUseMessage: (input) => summarizeToolArgs(input) || " ",
  };
  cache.set(name, tool);
  return tool;
}
