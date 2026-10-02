import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type Dispatch,
  type SetStateAction,
} from "react";
import { useInput, useApp } from "../cc/kepler-ink.js";
import type { ScrollBoxHandle } from "../cc/kepler-ink.js";
import {
  computeUnseenDivider,
  useUnseenDivider,
} from "../cc/components/FullscreenLayout.js";
import {
  AppServerClient,
  extractMessageText,
  itemsToMessages,
  parseAssistantCompleted,
  type ApprovalRequest,
  type ServerItem,
  type ToolEvent,
} from "../client/appServer.js";
import { spawnBackend } from "../backend/spawn.js";
import { toolDisplayName } from "../lib/toolDisplay.js";
import type { RenderableMessage } from "../cc/types/message.js";
import {
  createAssistantMessage,
  createSystemMessage,
  createUserMessage,
} from "../cc/utils/messages.js";
import { isHumanTurn } from "../cc/utils/messagePredicates.js";

/** CC REPL: typing into an empty prompt re-pins unless user scrolled recently. */
const RECENT_SCROLL_REPIN_WINDOW_MS = 3000;

type ReplConfig = {
  cwd: string;
  model: string;
  user: string;
  sessionId?: string;
  resume: boolean;
  inputRouting: "steer" | "queue";
};

type ConnectionState = "connecting" | "ready" | "failed";

export type ActiveTool = {
  id: string;
  name: string;
  detail: string;
};

export function useRepl(config: ReplConfig) {
  const { exit } = useApp();
  const [messages, setMessages] = useState<RenderableMessage[]>([]);
  // CC REPL pattern: streaming text is separate from messages[] — rendered
  // below VirtualMessageList, not appended as a virtual assistant message.
  const [streamingText, setStreamingText] = useState<string | null>(null);
  const streamTextRef = useRef("");
  const finalAssistantRef = useRef("");
  const [inProgressToolUseIDs, setInProgressToolUseIDs] = useState<Set<string>>(() => new Set());
  const [activeTools, setActiveTools] = useState<ActiveTool[]>([]);
  const [busy, setBusy] = useState(false);
  // Reserve synchronously; React state and server notifications arrive later.
  const busyRef = useRef(false);
  const activeTurnRef = useRef<string | null>(null);
  const startGeneration = useRef(0);
  const [queuePaused, setQueuePaused] = useState(false);
  const [connectionState, setConnectionState] = useState<ConnectionState>("connecting");
  const [sessionId, setSessionId] = useState<string | null>(config.sessionId ?? null);
  const [activeTurnId, setActiveTurnId] = useState<string | null>(null);
  const [approval, setApproval] = useState<ApprovalRequest | null>(null);
  const scrollRef = useRef<ScrollBoxHandle | null>(null);
  const [queued, setQueued] = useState<string[]>([]);
  const lastUserScrollTsRef = useRef(0);

  // CC REPL.tsx — useUnseenDivider + composedOnScroll on ScrollKeybindingHandler.
  const { dividerIndex, dividerYRef, onScrollAway, onRepin, jumpToNew } =
    useUnseenDivider(messages.length);
  const unseenDivider = useMemo(
    () => computeUnseenDivider(messages, dividerIndex),
    [dividerIndex, messages.length],
  );

  const repinScroll = useCallback(() => {
    scrollRef.current?.scrollToBottom();
    onRepin();
  }, [onRepin]);

  const composedOnScroll = useCallback(
    (sticky: boolean, handle: ScrollBoxHandle) => {
      lastUserScrollTsRef.current = Date.now();
      if (sticky) {
        onRepin();
      } else {
        onScrollAway(handle);
      }
    },
    [onRepin, onScrollAway],
  );

  const repinOnPromptInput = useCallback(
    (wasEmpty: boolean, next: string) => {
      if (
        wasEmpty &&
        next !== "" &&
        Date.now() - lastUserScrollTsRef.current >= RECENT_SCROLL_REPIN_WINDOW_MS
      ) {
        repinScroll();
      }
    },
    [repinScroll],
  );

  const onStreamingText = useCallback(
    (f: (current: string | null) => string | null) => {
      setStreamingText(f);
    },
    [],
  );

  const pushSystem = useCallback((text: string) => {
    setMessages((prev) => [...prev, createSystemMessage(text, "info")]);
  }, []);

  const client = useMemo(() => {
    const backend = spawnBackend();
    const connection = new AppServerClient(backend.stdin, backend.stdout, {
      onDisconnect: (error) => {
        busyRef.current = false;
        activeTurnRef.current = null;
        setConnectionState("failed");
        setBusy(false);
        setActiveTurnId(null);
        setApproval(null);
        setActiveTools([]);
        setInProgressToolUseIDs(new Set());
        setQueuePaused(true);
        pushSystem(error.message);
      },
      onDelta: (turnId, text) => {
        if (activeTurnRef.current !== turnId) return;
        streamTextRef.current += text;
        onStreamingText((current) => (current ?? "") + text);
      },
      onTurnStarted: (turnId) => {
        activeTurnRef.current = turnId;
        busyRef.current = true;
        setActiveTurnId(turnId);
        setBusy(true);
        finalAssistantRef.current = "";
        streamTextRef.current = "";
        setStreamingText(null);
        setInProgressToolUseIDs(new Set());
        setActiveTools([]);
      },
      onTurnCompleted: (turnId, payload) => {
        if (activeTurnRef.current !== turnId) return;
        activeTurnRef.current = null;
        busyRef.current = false;
        setApproval(null);
        const streamed = streamTextRef.current;
        const payloadMessage = payload.message;
        const fromPayload =
          payloadMessage && typeof payloadMessage === "object"
            ? extractMessageText(payloadMessage as Record<string, unknown>)
            : "";
        const text = pickAssistantText(streamed, fromPayload, finalAssistantRef.current);
        if (text.length > 0) {
          setMessages((prev) => [...prev, createAssistantMessage({ content: text })]);
        }
        streamTextRef.current = "";
        setStreamingText(null);
        setActiveTurnId(null);
        setBusy(false);
        setInProgressToolUseIDs(new Set());
        setActiveTools([]);
        finalAssistantRef.current = "";
        if (payload.error) {
          pushSystem(String(payload.error));
        }
      },
      onApproval: (request) => {
        if (activeTurnRef.current === request.turnId) setApproval(request);
      },
      onTool: (event) => {
        if (activeTurnRef.current === event.turnId) handleToolEvent(event, setMessages, setInProgressToolUseIDs, setActiveTools);
      },
      onItem: (method, params) => {
        if (method !== "item/completed") {
          return;
        }
        const record = (params ?? {}) as Record<string, unknown>;
        if (record.turnId !== activeTurnRef.current) return;
        const text = parseAssistantCompleted(record);
        if (text) {
          finalAssistantRef.current = text;
        }
      },
    });
    backend.onExit((code, signal) => connection.close(new Error(`app-server stopped (${code ?? signal ?? "error"})`)));
    return connection;
  }, [onStreamingText, pushSystem]);

  const startTurn = useCallback(
    async (text: string): Promise<boolean> => {
      if (!sessionId || busyRef.current || connectionState !== "ready") return false;
      busyRef.current = true;
      const generation = ++startGeneration.current;
      setBusy(true);
      const message = createUserMessage({ content: text });
      setMessages((prev) => [...prev, message]);
      try {
        await client.startTurn(sessionId, text);
        return true;
      } catch (error) {
        setMessages((prev) => prev.filter((item) => item !== message));
        pushSystem(`turn failed: ${(error as Error).message}`);
        setQueuePaused(true);
        if (generation === startGeneration.current) {
          busyRef.current = false;
          setBusy(false);
        }
        return false;
      }
    },
    [client, connectionState, pushSystem, sessionId],
  );

  useEffect(() => {
    if (busyRef.current || busy || queuePaused || connectionState !== "ready" || queued.length === 0) return;
    const [next, ...rest] = queued;
    const started = startTurn(next);
    setQueued(rest);
    void started.then((accepted) => {
      if (!accepted) {
        setQueued((pending) => [next, ...pending]);
        setQueuePaused(true);
        pushSystem("queued message retained; submit a message to resume the queue");
      }
    });
  }, [busy, queued, queuePaused, connectionState, startTurn, pushSystem]);

  useEffect(() => {
    let canceled = false;
    (async () => {
      try {
        await client.initialize();
        let id = config.sessionId ?? "";
        if (config.resume && id) {
          const items = await client.resumeThread(id);
          if (!canceled) {
            setSessionId(id);
            setConnectionState("ready");
            setMessages(itemsToRenderable(items));
            pushSystem(`resumed session ${id}`);
            queueMicrotask(repinScroll);
          }
          return;
        }
        id = await client.startThread(id || undefined);
        if (!canceled) {
          setSessionId(id);
          setConnectionState("ready");
        }
      } catch (error) {
        if (!canceled) {
          setConnectionState("failed");
          pushSystem(`failed to connect: ${(error as Error).message}`);
        }
      }
    })();
    return () => {
      canceled = true;
    };
  }, [client, config.resume, config.sessionId, pushSystem, repinScroll]);

  const lastMsg = messages.at(-1);
  const lastMsgIsHuman = lastMsg != null && isHumanTurn(lastMsg);
  useEffect(() => {
    if (lastMsgIsHuman) {
      repinScroll();
    }
  }, [lastMsg, lastMsgIsHuman, repinScroll]);

  const runSlash = useCallback(
    async (text: string): Promise<boolean> => {
      switch (text.split(/\s+/)[0]) {
        case "/help":
          pushSystem("Commands: /help /status /clear /exit · wheel/PgUp/PgDn scroll · Ctrl+End jump to bottom");
          return true;
        case "/status":
          pushSystem(
            `model ${config.model} · workspace ${config.cwd} · session ${sessionId ?? "—"} · routing ${config.inputRouting}`,
          );
          return true;
		case "/clear":
			if (busy) {
				pushSystem("stop the active turn before starting a fresh session");
				return true;
			}
			setSessionId(await client.startThread());
			setMessages([]);
          setStreamingText(null);
          streamTextRef.current = "";
          repinScroll();
          return true;
        case "/exit":
        case "/quit":
          exit();
          return true;
        default:
          pushSystem(`unknown command ${text}`);
          return true;
      }
	},
	[busy, client, config.cwd, config.inputRouting, config.model, exit, pushSystem, repinScroll, sessionId],
  );

  const submitText = useCallback(
    async (text: string): Promise<boolean> => {
      const trimmed = text.trim();
      if (!trimmed) return false;
      if (connectionState !== "ready" || !sessionId) {
        pushSystem(connectionState === "connecting" ? "still connecting to app-server…" : "app-server is disconnected; restart the CLI to reconnect");
        return false;
      }
      if (trimmed.startsWith("/")) {
        try { return await runSlash(trimmed); }
        catch (error) { pushSystem((error as Error).message); return false; }
      }
      if (busyRef.current) {
        const turnId = activeTurnRef.current;
        if (config.inputRouting === "steer" && turnId) {
          try {
            await client.steerTurn(turnId, trimmed);
            repinScroll();
            setMessages((prev) => [...prev, createUserMessage({ content: trimmed })]);
            return true;
          } catch (error) {
            pushSystem(`steer failed: ${(error as Error).message}`);
            return false;
          }
        }
        setQueued((pending) => [...pending, trimmed]);
        pushSystem("follow-up queued");
        return true;
      }
      if (queued.length > 0) {
        setQueued((pending) => [...pending, trimmed]);
        setQueuePaused(false);
        return true;
      }
      repinScroll();
      return startTurn(trimmed);
    },
    [client, config.inputRouting, connectionState, pushSystem, queued.length, repinScroll, sessionId, startTurn, runSlash],
  );

  const respondApproval = useCallback(
    async (scope: string) => {
      if (!approval || !sessionId) {
        return;
      }
      try {
        await client.respondApproval(approval.turnId, sessionId, approval.toolCallId, scope);
        pushSystem(`approval ${scope} for ${approval.toolName}`);
        setApproval(null);
      } catch (error) {
        pushSystem(`approval failed: ${(error as Error).message}`);
      }
    },
    [approval, client, pushSystem, sessionId],
  );

  // Scroll / cancel shortcuts — mirrors CC CancelRequestHandler scoping:
  // Ctrl+C at idle prompt is owned by useTextInput (double-press to exit).
  // Only claim Ctrl+C here while a turn is running.
  useInput((inputKey, key, event) => {
    if (approval) {
      if (inputKey === "o" || inputKey === "y") {
        void respondApproval("once");
        event.stopImmediatePropagation();
        return;
      }
      if (inputKey === "n") {
        void respondApproval("deny");
        event.stopImmediatePropagation();
        return;
      }
      if (inputKey === "s") {
        void respondApproval("session");
        event.stopImmediatePropagation();
        return;
      }
      if (inputKey === "p") {
        void respondApproval("project");
        event.stopImmediatePropagation();
        return;
      }
      return;
    }
    if (key.ctrl && inputKey === "c" && busy && activeTurnId) {
      void client.cancelTurn(activeTurnId).then(
        () => pushSystem("cancellation requested"),
        (error) => pushSystem(`cancel failed: ${(error as Error).message}`),
      );
      event.stopImmediatePropagation();
      return;
    }
  });

  return {
    messages,
    streamingText,
    inProgressToolUseIDs,
    activeTools,
    submitText,
    busy,
    approval,
    sessionId,
    connectionState,
    scrollRef,
    composedOnScroll,
    dividerYRef,
    jumpToNew,
    unseenDivider,
    repinOnPromptInput,
    config,
  };
}

function handleToolEvent(
  event: ToolEvent,
  setMessages: Dispatch<SetStateAction<RenderableMessage[]>>,
  setInProgress: Dispatch<SetStateAction<Set<string>>>,
  setActiveTools: Dispatch<SetStateAction<ActiveTool[]>>,
): void {
  if (event.status === "running") {
    setInProgress((prev) => new Set(prev).add(event.toolCallId));
    setActiveTools((prev) => [
      ...prev.filter((tool) => tool.id !== event.toolCallId),
      { id: event.toolCallId, name: event.toolName, detail: event.detail },
    ]);
    return;
  }
  setInProgress((prev) => {
    const next = new Set(prev);
    next.delete(event.toolCallId);
    return next;
  });
  setActiveTools((prev) => prev.filter((tool) => tool.id !== event.toolCallId));
  pushSystemMessage(setMessages, formatToolEvent(event, event.status));
}

function pushSystemMessage(
  setMessages: Dispatch<SetStateAction<RenderableMessage[]>>,
  text: string,
): void {
  setMessages((prev) => [...prev, createSystemMessage(text, "info")]);
}

function formatToolEvent(event: ToolEvent, status: ToolEvent["status"]): string {
  const label = toolDisplayName(event.toolName);
  const detail = event.detail ? " · " + event.detail : "";
  switch (status) {
    case "running":
      return label + detail;
    case "done":
      return "✓ " + label + " completed" + detail;
    case "failed":
      return "✗ " + label + " failed" + detail;
  }
}

function pickAssistantText(streamed: string, fromPayload: string, fromItem: string): string {
  // Durable final content is authoritative, even when shorter than the stream.
  return fromPayload.trimEnd() || fromItem.trimEnd() || streamed.trimEnd();
}

function itemsToRenderable(items: ServerItem[]): RenderableMessage[] {
  const out: RenderableMessage[] = [];
  for (const item of itemsToMessages(items)) {
    switch (item.kind) {
      case "user":
        out.push(createUserMessage({ content: item.text }));
        break;
      case "assistant":
        out.push(createAssistantMessage({ content: item.text }));
        break;
      default:
        if (item.text) {
          out.push(createSystemMessage(item.text, "info"));
        }
        break;
    }
  }
  return out;
}
