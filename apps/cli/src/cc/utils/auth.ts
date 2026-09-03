/** Kepler stub: OAuth/auth chain not needed for Messages rendering. */
export function isClaudeAISubscriber(): boolean { return false; }
export async function getAuthHeaders(): Promise<Record<string, string>> { return {}; }
