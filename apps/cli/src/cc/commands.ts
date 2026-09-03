/**
 * Kepler stub: Messages only needs the Command type and an empty command list.
 * Full CC commands barrel imports 80+ command modules — not needed for Kepler transcript.
 */
import type { ReactNode } from "react";

export type Command = {
  name: string;
  description?: string;
  aliases?: string[];
  argumentHint?: string;
  isEnabled?: boolean;
  isHidden?: boolean;
  userFacingName?: () => string;
  call?: (...args: unknown[]) => Promise<ReactNode | string | void>;
};

export const commands: Command[] = [];
