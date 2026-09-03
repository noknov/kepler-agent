/** Kepler stub: attachment types for messages.ts (full attachments.ts is 4000+ lines). */
export type Attachment = {
  type: string;
  path?: string;
  content?: string;
  [key: string]: unknown;
};

export type HookAttachment = { type: string; hookName?: string };
export type HookPermissionDecisionAttachment = { type: string };

export function memoryHeader(_title: string): string {
  return "";
}
