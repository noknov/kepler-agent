/**
 * Kepler stub: StatusNotices/Messages use AgentDefinitionsResult type only.
 */
export type AgentDefinitionsResult = {
  agents: Array<{ name: string; description?: string }>;
  loadedAt?: number;
};
