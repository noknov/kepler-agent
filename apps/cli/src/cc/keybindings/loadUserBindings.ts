/**
 * Kepler stub: default bindings only (no ~/.claude/keybindings.json hot-reload).
 */
import { DEFAULT_BINDINGS } from "./defaultBindings.js";
import { parseBindings } from "./parser.js";
import type { ParsedBinding } from "./types.js";
import { type KeybindingWarning, validateBindings } from "./validate.js";

export type KeybindingsLoadResult = {
  bindings: ParsedBinding[];
  warnings: KeybindingWarning[];
};

export function isKeybindingCustomizationEnabled(): boolean {
  return false;
}

export function loadKeybindingsSyncWithWarnings(): KeybindingsLoadResult {
  const bindings = parseBindings(DEFAULT_BINDINGS);
  const warnings = validateBindings(DEFAULT_BINDINGS);
  return { bindings, warnings };
}

export function initializeKeybindingWatcher(): void {}

export function subscribeToKeybindingChanges(
  _listener: (result: KeybindingsLoadResult) => void,
): () => void {
  return () => undefined;
}
