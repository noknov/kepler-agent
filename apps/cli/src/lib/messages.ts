export type MessageKind = "user" | "assistant" | "system" | "tool" | "tool-done" | "steer";

export type Message = {
  id: string;
  kind: MessageKind;
  text: string;
  toolName?: string;
  status?: "running" | "done" | "failed";
};

let nextId = 0;

export function messageId(prefix: string): string {
  nextId += 1;
  return `${prefix}-${nextId}`;
}

export function flattenMessages(messages: Message[]): string[] {
  const lines: string[] = [];
  for (const message of messages) {
    const chunks = message.text.split("\n");
    for (const chunk of chunks) {
      lines.push(formatLine(message, chunk));
    }
  }
  return lines;
}

function formatLine(message: Message, text: string): string {
  switch (message.kind) {
    case "user":
      return `❯ ${text}`;
    case "assistant":
      return `◆ ${text}`;
    case "tool":
      return `⚙ ${message.toolName ?? "tool"} ${text}`;
    case "tool-done":
      return `⚙ ${message.toolName ?? "tool"} ${message.status === "failed" ? "✗" : "✓"} ${text}`;
    case "steer":
      return `↪ steered: ${text}`;
    default:
      return `· ${text}`;
  }
}
