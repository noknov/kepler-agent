/** Kepler stub: settings loader (full settings.ts pulls plugins/MCP/oauth). */
export function getInitialSettings(): { advisorModel?: string } {
  return {};
}

export function getSettings_DEPRECATED(): Record<string, unknown> {
  return {};
}

export function getSettingsWithErrors(): { settings: Record<string, unknown>; errors: unknown[] } {
  return { settings: {}, errors: [] };
}
