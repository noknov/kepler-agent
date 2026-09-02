import { useCallback, useEffect, useMemo, useState, type Dispatch, type SetStateAction } from "react";
import { useApp, useInput, useStdout } from "ink";
import {
  AppServerClient,
  itemsToMessages,
  type ApprovalRequest,
  type ServerItem,
  type ToolEvent,
} from "../client/appServer.js";
import { spawnBackend } from "../backend/spawn.js";
import { messageId, type Message, type MessageKind } from "../lib/messages.js";
import { filterSlashCommands } from "../lib/slashCommands.js";
import { randomSpinnerVerb } from "../lib/spinner.js";

type ReplConfig = {
  cwd: string;
  model: string;
  user: string;
  sessionId?: string;
  resume: boolean;
  inputRouting: "steer" | "queue";
};

export function useRepl(config: ReplConfig) {
  const { exit } = useApp();
  const { stdout } = useStdout();
  const [messages, setMessages] = useState<Message[]>([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [sessionId, setSessionId] = useState<string | null>(config.sessionId ?? null);
  const [activeTurnId, setActiveTurnId] = useState<string | null>(null);
  const [streamText, setStreamText] = useState("");
  const [approval, setApproval] = useState<ApprovalRequest | null>(null);
  const [scrollOffset, setScrollOffset] = useState(0);
  const [unseen, setUnseen] = useState(0);
  const [spinnerFrame, setSpinnerFrame] = useState(0);
  const [spinnerVerb] = useState(() => randomSpinnerVerb());
  const [queued, setQueued] = useState<string[]>([]);

  const pushMessage = useCallback((kind: MessageKind, text: string, extra?: Partial<Message>) => {
    setMessages((prev) => [...prev, { id: messageId(kind), kind, text, ...extra }]);
  }, []);

  const pushSystem = useCallback(
    (text: string) => {
      pushMessage("system", text);
    },
    [pushMessage],
  );

  const client = useMemo(() => {
    const backend = spawnBackend();
    return new AppServerClient(backend.stdin, backend.stdout, {
      onDelta: (_turnId, text) => {
        setStreamText((current) => current + text);
        setScrollOffset((offset) => {
          if (offset > 0) {
            setUnseen((count) => count + 1);
          }
          return offset;
        });
      },
      onTurnStarted: (turnId) => {
        setActiveTurnId(turnId);
        setBusy(true);
        setStreamText("");
      },
      onTurnCompleted: (_turnId, payload) => {
        setStreamText((current) => {
          if (current.trim()) {
            setMessages((prev) => [...prev, { id: messageId("assistant"), kind: "assistant", text: current }]);
          }
          return "";
        });
        setActiveTurnId(null);
        setBusy(false);
        if (payload.error) {
          pushSystem(String(payload.error));
        }
      },
      onApproval: (request) => setApproval(request),
      onTool: (event) => handleToolEvent(event, setMessages),
      onItem: () => undefined,
    });
    // client is stable for the session lifetime
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const startTurn = useCallback(
    async (text: string) => {
      if (!sessionId) {
        return;
      }
      try {
        await client.startTurn(sessionId, text);
      } catch (error) {
        pushSystem(`turn failed: ${(error as Error).message}`);
        setBusy(false);
      }
    },
    [client, pushSystem, sessionId],
  );

  useEffect(() => {
    if (busy || queued.length === 0) {
      return;
    }
    const [next, ...rest] = queued;
    setQueued(rest);
    pushMessage("user", next);
    void startTurn(next);
  }, [busy, queued, pushMessage, startTurn]);

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
            for (const item of itemsToMessages(items)) {
              const kind = mapKind(item.kind);
              pushMessage(kind, item.text, item.toolName ? { toolName: item.toolName } : undefined);
            }
            pushSystem(`resumed session ${id}`);
          }
          return;
        }
        id = await client.startThread(id || undefined);
        if (!canceled) {
          setSessionId(id);
        }
      } catch (error) {
        if (!canceled) {
          pushSystem(`failed to connect: ${(error as Error).message}`);
        }
      }
    })();
    return () => {
      canceled = true;
    };
  }, [client, config.resume, config.sessionId, pushMessage, pushSystem]);

  useEffect(() => {
    if (!busy) {
      return;
    }
    const timer = setInterval(() => setSpinnerFrame((frame) => frame + 1), 120);
    return () => clearInterval(timer);
  }, [busy]);

  const jumpToBottom = useCallback(() => {
    setScrollOffset(0);
    setUnseen(0);
  }, []);

  const submit = useCallback(async () => {
    const text = input.trim();
    if (!text || !sessionId) {
      return;
    }
    if (text.startsWith("/")) {
      const handled = await runSlash(text);
      if (handled) {
        setInput("");
      }
      return;
    }
    if (busy) {
      if (config.inputRouting === "steer" && activeTurnId) {
        pushMessage("steer", text);
        setInput("");
        try {
          await client.steerTurn(activeTurnId, text);
        } catch (error) {
          pushSystem(`steer failed: ${(error as Error).message}`);
        }
        return;
      }
      if (config.inputRouting === "queue") {
        setQueued((pending) => [...pending, text]);
        pushSystem(`queued (${queued.length + 1} waiting)`);
        setInput("");
        return;
      }
      return;
    }
    pushMessage("user", text);
    setInput("");
    await startTurn(text);
  }, [
    activeTurnId,
    busy,
    client,
    config.inputRouting,
    input,
    pushMessage,
    pushSystem,
    queued.length,
    sessionId,
    startTurn,
  ]);

  const runSlash = useCallback(
    async (text: string): Promise<boolean> => {
      switch (text.split(/\s+/)[0]) {
        case "/help":
          pushSystem("Commands: /help /status /clear /exit · PgUp/PgDn scroll · g jump to bottom");
          return true;
        case "/status":
          pushSystem(
            `model ${config.model} · workspace ${config.cwd} · session ${sessionId ?? "—"} · routing ${config.inputRouting}`,
          );
          return true;
        case "/clear":
          setMessages([]);
          setStreamText("");
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
    [config.cwd, config.inputRouting, config.model, exit, pushSystem, sessionId],
  );

  const respondApproval = useCallback(
    async (scope: string) => {
      if (!approval || !sessionId) {
        return;
      }
      try {
        await client.respondApproval(approval.turnId, sessionId, approval.toolCallId, scope);
        pushSystem(`approval ${scope} for ${approval.toolName}`);
      } catch (error) {
        pushSystem(`approval failed: ${(error as Error).message}`);
      } finally {
        setApproval(null);
      }
    },
    [approval, client, pushSystem, sessionId],
  );

  useInput((inputKey, key) => {
    if (approval) {
      if (inputKey === "o" || inputKey === "y") {
        void respondApproval("once");
        return;
      }
      if (inputKey === "n") {
        void respondApproval("deny");
        return;
      }
      if (inputKey === "s") {
        void respondApproval("session");
        return;
      }
      if (inputKey === "p") {
        void respondApproval("project");
        return;
      }
      return;
    }
    if (inputKey === "g") {
      jumpToBottom();
      return;
    }
    if (key.ctrl && inputKey === "c") {
      if (busy && activeTurnId) {
        void client.cancelTurn(activeTurnId);
        pushSystem("turn canceled");
        return;
      }
      exit();
      return;
    }
    if (key.pageUp) {
      setScrollOffset((offset) => offset + 3);
      return;
    }
    if (key.pageDown) {
      setScrollOffset((offset) => Math.max(offset - 3, 0));
      return;
    }
    if (key.upArrow) {
      setScrollOffset((offset) => offset + 1);
      return;
    }
    if (key.downArrow) {
      setScrollOffset((offset) => Math.max(offset - 1, 0));
      return;
    }
    if (key.return) {
      void submit();
      return;
    }
    if (key.backspace || key.delete) {
      setInput((current) => current.slice(0, -1));
      return;
    }
    if (inputKey && !key.ctrl && !key.meta && inputKey.length === 1) {
      setInput((current) => current + inputKey);
    }
  });

  const slashMatches = filterSlashCommands(input);
  const showSlash = input.startsWith("/") && slashMatches.length > 0 && !busy;

  return {
    messages,
    streamText,
    input,
    busy,
    approval,
    sessionId,
    scrollOffset,
    unseen,
    spinnerFrame,
    spinnerVerb,
    showSlash,
    slashMatches,
    jumpToBottom,
    stdout,
    config,
  };
}

function handleToolEvent(event: ToolEvent, setMessages: Dispatch<SetStateAction<Message[]>>): void {
  setMessages((prev) => {
    if (event.status === "running") {
      return [
        ...prev,
        {
          id: messageId("tool"),
          kind: "tool",
          text: event.detail,
          toolName: event.toolName,
          status: "running",
        },
      ];
    }
    const without = prev.filter(
      (message) => !(message.kind === "tool" && message.toolName === event.toolName && message.status === "running"),
    );
    return [
      ...without,
      {
        id: messageId("tool"),
        kind: "tool-done",
        text: event.detail || (event.status === "failed" ? "failed" : "done"),
        toolName: event.toolName,
        status: event.status === "failed" ? "failed" : "done",
      },
    ];
  });
}

function mapKind(kind: string): MessageKind {
  switch (kind) {
    case "user":
      return "user";
    case "assistant":
      return "assistant";
    case "tool":
      return "tool";
    case "tool-done":
    case "tool-failed":
      return "tool-done";
    default:
      return "system";
  }
}
