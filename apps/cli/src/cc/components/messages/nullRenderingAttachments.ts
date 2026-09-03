import type { Message, NormalizedMessage } from "../types/message.js";

export function isNullRenderingAttachment(_msg: Message | NormalizedMessage): boolean {
  return false;
}
