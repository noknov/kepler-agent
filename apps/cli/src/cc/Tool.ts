/**
 * Kepler stub: Messages/MessageRow need Tool types and findToolByName at compile time.
 * Runtime tool rendering uses tools passed via props from Kepler.
 */
import type { ReactNode } from "react";
import type { z } from "zod";
import { getKeplerToolFallback } from "./kepler/toolFallback.js";

export type AnyObject = z.ZodType<{ [key: string]: unknown }>;

export type ToolProgressData = Record<string, unknown>;

export type Tool<
  Input extends AnyObject = AnyObject,
  Output = unknown,
> = {
  name: string;
  inputSchema?: Input;
  outputSchema?: { safeParse: (v: unknown) => { success: boolean; data?: Output } };
  userFacingName?: (input?: z.infer<Input>) => string;
  renderToolUseMessage?: (...args: unknown[]) => ReactNode;
  renderToolResultMessage?: (...args: unknown[]) => ReactNode;
  renderToolUseProgressMessage?: (...args: unknown[]) => ReactNode;
  renderToolUseRejectedMessage?: (...args: unknown[]) => ReactNode;
  renderToolUseErrorMessage?: (...args: unknown[]) => ReactNode;
  isReadOnly?: (input: z.infer<Input>) => boolean;
  isCollapsible?: boolean;
};

export type Tools = readonly Tool[];

export function findToolByName(tools: Tools, name: string): Tool | undefined {
  const found = tools.find((t) => t.name === name);
  if (found) {
    return found;
  }
  return getKeplerToolFallback(name);
}

export function filterToolProgressMessages<T>(messages: T[]): T[] {
  return messages;
}

export function toolMatchesName(tool: Tool, name: string): boolean {
  return tool.name === name;
}
