import { createInterface } from "node:readline";
import type { Readable, Writable } from "node:stream";
import type { NotificationMethod, NotificationParams, ProtocolMethod, ProtocolParams, ProtocolResults } from "../generated/appServerProtocol.js";
import { summarizeToolArgs } from "../lib/toolDisplay.js";

type JsonRpcRequest = {
  jsonrpc: "2.0";
  id?: number | string;
  method: string;
  params?: unknown;
};

type JsonRpcResponse = {
  jsonrpc: "2.0";
  id?: number | string;
  result?: unknown;
  error?: { code: number; message: string };
};

type JsonRpcNotification<M extends NotificationMethod = NotificationMethod> = {
  jsonrpc: "2.0";
  method: M;
  params?: NotificationParams[M];
};

export type ApprovalRequest = {
  turnId: string;
  sessionId: string;
  toolCallId: string;
  toolName: string;
  summary: string;
  reason?: string;
};

export type ToolEvent = {
  turnId: string;
  toolCallId: string;
  toolName: string;
  status: "running" | "done" | "failed";
  detail: string;
};

export type TranscriptItem = {
  eventType: string;
  payload: Record<string, unknown>;
};

export type ServerItem = {
  eventType?: string;
  type?: string;
  payload?: Record<string, unknown> | string;
};

export type AppServerEvents = {
  onDelta: (turnId: string, text: string) => void;
  onTurnStarted: (turnId: string, sessionId: string) => void;
  onTurnCompleted: (turnId: string, payload: Record<string, unknown>) => void;
  onApproval: (request: ApprovalRequest) => void;
  onTool: (event: ToolEvent) => void;
  onItem: (method: string, params: unknown) => void;
};

export class AppServerRPCError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(message);
    this.name = "AppServerRPCError";
  }
}

export class AppServerClient {
  private nextId = 1;
  private pending = new Map<number, { resolve: (value: unknown) => void; reject: (error: Error) => void }>();

  constructor(
    private readonly stdin: Writable,
    stdout: Readable,
    private readonly events: AppServerEvents,
  ) {
    const reader = createInterface({ input: stdout });
    reader.on("line", (line: string) => this.handleLine(line));
  }

  async initialize(timeoutMs = 10_000): Promise<void> {
    const result = await this.request("initialize", { clientName: "kepler-cli" }, timeoutMs);
    if (result.protocol !== "v2" || result.minimumProtocolVersion !== 2 || result.maximumProtocolVersion !== 2) {
      throw new Error(`unsupported app-server protocol: ${result.protocol ?? "unknown"}`);
    }
    this.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method: "initialized" })}\n`);
  }

  async startThread(sessionId?: string): Promise<string> {
    const result = await this.request("thread/start", sessionId ? { sessionId } : {});
    return result.sessionId;
  }

  async resumeThread(sessionId: string): Promise<ServerItem[]> {
    const result = await this.request("thread/resume", {
      sessionId,
      includeEvents: true,
    });
    return ((result.items ?? []) as ServerItem[]).map(normalizeItem);
  }

  async trajectory(sessionId: string, afterSequence = 0): Promise<ServerItem[]> {
    const result = await this.request("thread/trajectory", { sessionId, afterSequence });
    return (result.items as ServerItem[]).map(normalizeItem);
  }

  async startTurn(sessionId: string, input: string): Promise<string> {
    const result = await this.requestWithOverloadRetry("turn/start", { sessionId, input });
    return result.turnId;
  }

  async steerTurn(turnId: string, text: string): Promise<void> {
    await this.request("turn/steer", { turnId, text });
  }

  async cancelTurn(turnId: string): Promise<void> {
    await this.request("turn/interrupt", { turnId });
  }

  async respondApproval(turnId: string, sessionId: string, toolCallId: string, scope: string): Promise<void> {
    await this.request("approval/respond", { turnId, sessionId, toolCallId, scope });
  }

  private request<M extends ProtocolMethod>(method: M, params: ProtocolParams[M], timeoutMs = 30_000): Promise<ProtocolResults[M]> {
    const id = this.nextId++;
    const payload: JsonRpcRequest = { jsonrpc: "2.0", id, method, params };
    return new Promise<ProtocolResults[M]>((resolve, reject) => {
      const timer = setTimeout(() => {
        if (!this.pending.has(id)) {
          return;
        }
        this.pending.delete(id);
        reject(new Error(`${method} timed out after ${timeoutMs}ms`));
      }, timeoutMs);
      this.pending.set(id, {
        resolve: (value) => {
          clearTimeout(timer);
          resolve(value as ProtocolResults[M]);
        },
        reject: (error) => {
          clearTimeout(timer);
          reject(error);
        },
      });
      this.stdin.write(`${JSON.stringify(payload)}\n`);
    });
  }

  private async requestWithOverloadRetry<M extends ProtocolMethod>(
    method: M,
    params: ProtocolParams[M],
    attempts = 3,
  ): Promise<ProtocolResults[M]> {
    for (let attempt = 1; ; attempt++) {
      try {
        return await this.request(method, params);
      } catch (error) {
        if (!(error instanceof AppServerRPCError) || error.code !== -32001 || attempt >= attempts) {
          throw error;
        }
        const delayMs = 100 * 2 ** (attempt - 1) + Math.floor(Math.random() * 50);
        await new Promise((resolve) => setTimeout(resolve, delayMs));
      }
    }
  }

  private handleLine(line: string): void {
    if (!line.trim()) {
      return;
    }
    let message: JsonRpcResponse | JsonRpcNotification;
    try {
      message = JSON.parse(line) as JsonRpcResponse | JsonRpcNotification;
    } catch {
      return;
    }
    if ("id" in message && message.id !== undefined) {
      const waiter = this.pending.get(Number(message.id));
      if (!waiter) {
        return;
      }
      this.pending.delete(Number(message.id));
      if (message.error) {
        waiter.reject(new AppServerRPCError(message.error.code, message.error.message));
        return;
      }
      waiter.resolve(message.result);
      return;
    }
    if (!("method" in message) || !message.method) {
      return;
    }
    this.dispatchNotification(message.method, message.params);
  }

  private dispatchNotification(method: string, params: unknown): void {
    this.events.onItem(method, params);
    const record = (params ?? {}) as Record<string, unknown>;
    switch (method) {
      case "turn/started": {
        const turnId = String(record.turnId ?? "");
        const sessionId = String(record.sessionId ?? "");
        if (turnId) {
          this.events.onTurnStarted(turnId, sessionId);
        }
        break;
      }
      case "turn/completed": {
        const turnId = String(record.turnId ?? "");
        if (turnId) {
          this.events.onTurnCompleted(turnId, record);
        }
        break;
      }
      case "item/agentMessage/delta": {
        const turnId = String(record.turnId ?? "");
        const delta = String(record.delta ?? "");
        if (delta) {
          this.events.onDelta(turnId, delta);
        }
        break;
      }
      case "item/approvalRequested": {
        const approval = parseApproval(record);
        if (approval) {
          this.events.onApproval(approval);
        }
        break;
      }
      case "item/started":
      case "item/completed":
      case "item/updated": {
        const tool = parseToolEvent(record, method);
        if (tool) {
          this.events.onTool(tool);
        }
        break;
      }
      default:
        break;
    }
  }
}

function normalizeItem(item: ServerItem): ServerItem {
  const raw = item as ServerItem;
  const payload = raw.payload;
  if (payload && typeof payload === "object" && !Array.isArray(payload)) {
    return { eventType: String(raw.eventType ?? raw.type ?? ""), payload };
  }
  if (typeof payload === "string") {
    try {
      return { eventType: String(raw.eventType ?? raw.type ?? ""), payload: JSON.parse(payload) as Record<string, unknown> };
    } catch {
      return { eventType: String(raw.eventType ?? raw.type ?? ""), payload: {} };
    }
  }
  return { eventType: String(raw.eventType ?? raw.type ?? ""), payload: {} };
}

function parseApproval(params: Record<string, unknown>): ApprovalRequest | null {
  const event = extractEvent(params);
  if (!event) {
    return null;
  }
  const toolCall = readToolCall(event);
  const turnId = readString(event, "turn_id", "turnId");
  const sessionId = readString(event, "session_id", "sessionId");
  if (!toolCall?.id || !turnId || !sessionId) {
    return null;
  }
  const metadata = event.metadata;
  let reason = "";
  if (typeof metadata === "string") {
    reason = metadata;
  } else if (metadata && typeof metadata === "object" && "reason" in metadata) {
    reason = String((metadata as Record<string, unknown>).reason ?? "");
  }
  const name = String(toolCall.name ?? "tool");
  const args = toolCall.arguments ? JSON.stringify(toolCall.arguments) : "";
  return {
    turnId,
    sessionId,
    toolCallId: String(toolCall.id),
    toolName: name,
    summary: args.length > 120 ? `${args.slice(0, 117)}...` : args,
    reason,
  };
}

function parseToolEvent(params: Record<string, unknown>, method: string): ToolEvent | null {
  const event = extractEvent(params);
  if (!event) {
    return null;
  }
  const eventType = String(event.type ?? params.eventType ?? "");
  if (!eventType.startsWith("tool_call")) {
    return null;
  }
  const toolCall = readToolCall(event);
  if (!toolCall?.id) {
    return null;
  }
  const status =
    eventType === "tool_call_started"
      ? "running"
      : eventType === "tool_call_failed"
        ? "failed"
        : "done";
  if (method === "item/started" && status !== "running") {
    return null;
  }
  const detail = summarizeTool(toolCall);
  return {
    turnId: readString(event, "turn_id", "turnId"),
    toolCallId: String(toolCall.id),
    toolName: String(toolCall.name ?? "tool"),
    status,
    detail,
  };
}

function extractEvent(params: Record<string, unknown>): Record<string, unknown> | null {
  const payload = params.payload;
  if (payload && typeof payload === "object") {
    return payload as Record<string, unknown>;
  }
  if (typeof payload === "string") {
    try {
      return JSON.parse(payload) as Record<string, unknown>;
    } catch {
      return null;
    }
  }
  return params;
}

function readToolCall(event: Record<string, unknown>): Record<string, unknown> | undefined {
  const toolCall = event.tool_call ?? event.toolCall;
  if (!toolCall || typeof toolCall !== "object") {
    return undefined;
  }
  return toolCall as Record<string, unknown>;
}

function readString(event: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = event[key];
    if (value !== undefined && value !== null && value !== "") {
      return String(value);
    }
  }
  return "";
}

function summarizeTool(toolCall: Record<string, unknown>): string {
  return summarizeToolArgs(toolCall.arguments);
}

export function itemsToMessages(items: ServerItem[]): Array<{ kind: string; text: string; toolName?: string }> {
  const messages: Array<{ kind: string; text: string; toolName?: string }> = [];
  for (const item of items) {
    const normalized = normalizeItem(item);
    const event = normalized.payload;
    if (!event || typeof event !== "object") {
      continue;
    }
    const type = String((event as Record<string, unknown>).type ?? normalized.eventType);
    const message = (event as Record<string, unknown>).message as Record<string, unknown> | undefined;
    switch (type) {
      case "user_input":
      case "steering_input":
        messages.push({ kind: "user", text: extractMessageText(message) });
        break;
      case "assistant_message":
      case "model_completed":
        messages.push({ kind: "assistant", text: extractMessageText(message) });
        break;
      case "tool_call_started":
      case "tool_call_completed":
        break;
      case "tool_call_failed": {
        const toolCall = readToolCall(event as Record<string, unknown>);
        messages.push({
          kind: "tool-failed",
          toolName: String(toolCall?.name ?? "tool"),
          text: summarizeTool(toolCall ?? {}),
        });
        break;
      }
      default:
        break;
    }
  }
  return messages;
}

export function parseAssistantCompleted(params: Record<string, unknown>): string | null {
  const event = extractEvent(params);
  if (!event) {
    return null;
  }
  const eventType = String(event.type ?? params.eventType ?? "");
  if (eventType !== "assistant_message" && eventType !== "model_completed") {
    return null;
  }
  const message = event.message;
  if (!message || typeof message !== "object") {
    return null;
  }
  const text = extractMessageText(message as Record<string, unknown>).trim();
  return text.length > 0 ? text : null;
}

export function extractMessageText(message?: Record<string, unknown>): string {
  if (!message) {
    return "";
  }
  const content = message.content;
  if (Array.isArray(content)) {
    return content
      .map((part) => {
        if (!part || typeof part !== "object") {
          return "";
        }
        const block = part as Record<string, unknown>;
        const blockType = String(block.type ?? "");
        if (blockType && blockType !== "text" && !("text" in block)) {
          return "";
        }
        return String(block.text ?? "");
      })
      .join("");
  }
  return String(message.text ?? "");
}
