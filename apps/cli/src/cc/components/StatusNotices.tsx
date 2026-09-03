/**
 * Kepler stub: CC status notices (memory files, feature gates, MCP health).
 * Not needed for Kepler agent transcript.
 */
import type { AgentDefinitionsResult } from "../tools/AgentTool/loadAgentsDir.js";

type Props = {
  agentDefinitions?: AgentDefinitionsResult;
};

export function StatusNotices(_props: Props): null {
  return null;
}
