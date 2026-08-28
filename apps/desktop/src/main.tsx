import { invoke } from "@tauri-apps/api/core";
import { listen } from "@tauri-apps/api/event";
import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type Rpc = { jsonrpc: "2.0"; id: number; method: string; params?: Record<string, unknown> };
type ServerMessage = { jsonrpc?: string; method?: string; params?: Record<string, unknown>; result?: unknown; error?: { message?: string } };
type Entry = { id: string; kind: "user" | "assistant" | "tool" | "status"; text: string };
type PendingApproval = { turnId: string; toolCallId: string; label: string };

let rpcID = 1;
const rpc = (method: string, params?: Record<string, unknown>): Rpc => ({ jsonrpc: "2.0", id: rpcID++, method, params });

function eventPayload(params: Record<string, unknown> | undefined): Record<string, unknown> {
  const raw = params?.payload;
  if (typeof raw !== "string") return {};
  try { return JSON.parse(raw) as Record<string, unknown>; } catch { return {}; }
}

function App() {
  const [workspace, setWorkspace] = useState("");
  const [sessionID, setSessionID] = useState("");
  const [input, setInput] = useState("");
  const [connected, setConnected] = useState(false);
  const [activeTurn, setActiveTurn] = useState("");
  const [entries, setEntries] = useState<Entry[]>([{ id: "welcome", kind: "status", text: "Choose a local project folder to begin." }]);
  const [approval, setApproval] = useState<PendingApproval | null>(null);
  const bottom = useRef<HTMLDivElement>(null);

  const add = (entry: Entry) => setEntries((old) => [...old, entry]);
  useEffect(() => { bottom.current?.scrollIntoView({ behavior: "smooth" }); }, [entries]);

  useEffect(() => {
    const unlisten = listen<ServerMessage>("appserver-message", ({ payload }) => {
      if (payload.method === "item/agentMessage/delta") {
        const delta = String(payload.params?.delta ?? payload.params?.text ?? "");
        if (!delta) return;
        setEntries((old) => {
          const last = old.at(-1);
          if (last?.kind === "assistant" && last.id === "stream") return [...old.slice(0, -1), { ...last, text: last.text + delta }];
          return [...old, { id: "stream", kind: "assistant", text: delta }];
        });
      } else if (payload.method === "item/started" || payload.method === "item/completed") {
        const details = eventPayload(payload.params);
        const call = details.tool_call as Record<string, unknown> | undefined;
        const name = String(call?.name ?? "tool");
        add({ id: crypto.randomUUID(), kind: "tool", text: `${payload.method === "item/started" ? "Running" : "Finished"}: ${name}` });
      } else if (payload.method === "item/approvalRequested") {
        const details = eventPayload(payload.params);
        const call = details.tool_call as Record<string, unknown> | undefined;
        setApproval({ turnId: String(payload.params?.turnId ?? ""), toolCallId: String(call?.id ?? ""), label: String(call?.name ?? "Sensitive action") });
      } else if (payload.method === "turn/completed") {
        setActiveTurn("");
        const message = String(payload.params?.message ?? "");
        if (message) add({ id: crypto.randomUUID(), kind: "assistant", text: message });
      }
    });
    return () => { void unlisten.then((dispose) => dispose()); };
  }, []);

  async function send(request: Rpc) {
    return invoke<void>("send_rpc", { request });
  }

  async function connect() {
    const root = workspace.trim();
    if (!root) return;
    try {
      await invoke("start_server", { workspace: root });
      await send(rpc("initialize"));
      const createdSession = `ses-${crypto.randomUUID()}`;
      await send(rpc("thread/start", { sessionId: createdSession }));
      setSessionID(createdSession);
      setConnected(true);
      setEntries([{ id: "ready", kind: "status", text: `Ready in ${root}` }]);
    } catch (error) {
      add({ id: crypto.randomUUID(), kind: "status", text: `Could not start Kepler: ${String(error)}` });
    }
  }

  async function newConversation() {
    if (!connected) {
      setEntries([{ id: "welcome", kind: "status", text: "Choose a local project folder to begin." }]);
      return;
    }
    const nextSession = `ses-${crypto.randomUUID()}`;
    await send(rpc("thread/start", { sessionId: nextSession }));
    setSessionID(nextSession);
    setEntries([{ id: "new", kind: "status", text: "New local conversation." }]);
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const text = input.trim();
    if (!text || !connected || activeTurn) return;
    setInput("");
    add({ id: crypto.randomUUID(), kind: "user", text });
    const turnID = `turn-${crypto.randomUUID()}`;
    setActiveTurn(turnID);
    await send(rpc("turn/start", { sessionId: sessionID, turnId: turnID, input: text }));
  }

  async function resolve(scope: "once" | "deny") {
    if (!approval) return;
    await send(rpc("approval/resolve", { turnId: approval.turnId, toolCallId: approval.toolCallId, scope }));
    setApproval(null);
  }

  const status = useMemo(() => activeTurn ? "Working" : connected ? "Ready" : "Offline", [activeTurn, connected]);
  return <main className="shell">
    <aside className="sidebar">
      <div className="brand"><span>✦</span> KEPLER</div>
      <button className="new" onClick={() => void newConversation()}>＋ New conversation</button>
      <div className="sidebar-label">PROJECT</div>
      <input aria-label="Project directory" value={workspace} onChange={(event) => setWorkspace(event.target.value)} placeholder="/path/to/project" />
      <button className="connect" onClick={() => void connect()} disabled={!workspace.trim() || connected}>Open project</button>
      <div className="sidebar-label">LOCAL SESSIONS</div>
      <p className="muted">Sessions remain on this machine.</p>
      <div className="sidebar-foot"><i className={connected ? "dot on" : "dot"} /> {status}</div>
    </aside>
    <section className="conversation">
      <header><div><strong>{workspace || "No project selected"}</strong><span>{sessionID || "Local-only agent"}</span></div><button className="quiet" disabled={!activeTurn} onClick={() => void send(rpc("turn/interrupt", { turnId: activeTurn }))}>Stop</button></header>
      <div className="timeline">
        {entries.map((entry) => <article key={entry.id} className={`entry ${entry.kind}`}><div className="avatar">{entry.kind === "user" ? "You" : entry.kind === "assistant" ? "K" : "·"}</div><div>{entry.text}</div></article>)}
        <div ref={bottom} />
      </div>
      {approval && <div className="approval"><strong>{approval.label} needs your approval</strong><span>This action may change an external system.</span><div><button className="quiet" onClick={() => void resolve("deny")}>Deny</button><button className="allow" onClick={() => void resolve("once")}>Allow once</button></div></div>}
      <form className="composer" onSubmit={(event) => void submit(event)}><textarea value={input} onChange={(event) => setInput(event.target.value)} disabled={!connected || Boolean(activeTurn)} placeholder={connected ? "Ask Kepler to work in this project…" : "Open a project to start"} rows={2} /><button className="send" type="submit" disabled={!connected || !input.trim() || Boolean(activeTurn)}>Send ↵</button></form>
    </section>
    <aside className="inspector"><div className="sidebar-label">RUN SETTINGS</div><dl><dt>Surface</dt><dd>Desktop</dd><dt>Storage</dt><dd>Local JSONL</dd><dt>Step limit</dt><dd>Unlimited</dd><dt>Permissions</dt><dd>Ask on writes</dd></dl><p className="muted">Model and credentials use your local Kepler configuration.</p></aside>
  </main>;
}

createRoot(document.getElementById("root")!).render(<App />);
