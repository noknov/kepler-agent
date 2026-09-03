export type TextBlockParam = { type: "text"; text: string };
export type ToolUseBlockParam = { type: "tool_use"; id: string; name: string; input: unknown };
export type ContentBlockParam = TextBlockParam | ToolUseBlockParam;

export class APIUserAbortError extends Error {
  constructor(message = "aborted") {
    super(message);
    this.name = "APIUserAbortError";
  }
}
