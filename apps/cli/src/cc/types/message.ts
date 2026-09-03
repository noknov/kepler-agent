/** Minimal Claude Code message types for Kepler (full types are not in the CC checkout). */
import type { UUID } from "crypto";

export type { UUID };

export type TextBlock = { type: "text"; text: string };
export type ToolUseBlock = { type: "tool_use"; id: string; name: string; input: unknown };
export type ContentBlock = TextBlock | ToolUseBlock;

export type UserMessage = {
  type: "user";
  uuid: UUID;
  timestamp: string;
  message: {
    role: "user";
    content: ContentBlock[];
  };
  isMeta?: true;
  isVisibleInTranscriptOnly?: true;
};

export type AssistantMessage = {
  type: "assistant";
  uuid: UUID;
  timestamp: string;
  message: {
    role: "assistant";
    content: ContentBlock[];
  };
  isVirtual?: true;
};

export type SystemInformationalMessage = {
  type: "system";
  subtype: "informational";
  uuid: UUID;
  timestamp: string;
  content: string;
};

export type Message = UserMessage | AssistantMessage | SystemInformationalMessage;
export type NormalizedUserMessage = UserMessage;
export type NormalizedMessage = Message;
export type RenderableMessage = Message;
