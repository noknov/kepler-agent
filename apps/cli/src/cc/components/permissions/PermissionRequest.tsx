/**
 * Kepler stub: Messages imports ToolUseConfirm type only (not the permission UI).
 */
import type { ReactNode } from "react";
import type { AnyObject, Tool } from "../../Tool.js";
import type { AssistantMessage } from "../../types/message.js";

export type ToolUseConfirm<Input extends AnyObject = AnyObject> = {
  assistantMessage: AssistantMessage;
  tool: Tool<Input>;
  description: string;
  input: unknown;
  toolUseID: string;
  permissionResult: unknown;
  permissionPromptStartTimeMs: number;
  onUserInteraction(): void;
  onAbort(): void;
  onAllow(updatedInput: unknown, permissionUpdates: unknown[], feedback?: string): void;
  onReject(feedback?: string): void;
  recheckPermission(): Promise<void>;
};

export function PermissionRequest(_props: { confirms: ToolUseConfirm[] }): ReactNode {
  return null;
}
