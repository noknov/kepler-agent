/**
 * Kepler stub: connector text blocks used in Message.tsx behind feature flags.
 */
export type ConnectorTextBlock = {
  type: "connector_text";
  text: string;
};

export function isConnectorTextBlock(block: { type: string }): block is ConnectorTextBlock {
  return block.type === "connector_text";
}
