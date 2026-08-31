const $ = (selector) => document.querySelector(selector);
const state = { session: null, csrf: "", brand: { name: "Kepler", avatarUrl: "/assets/avatar.png" }, conversations: [], current: null, events: [], stream: null, running: false, maxSequence: 0 };

async function api(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (options.body) headers["Content-Type"] = "application/json";
  if (options.method && options.method !== "GET") headers["X-CSRF-Token"] = state.csrf;
  const response = await fetch(path, { credentials: "same-origin", ...options, headers });
  if (response.status === 204) return null;
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload?.error?.message || "Something went wrong");
  return payload;
}

async function boot() {
  await loadBrand();
  showAuthError();
  try {
    const payload = await api("/api/session");
    state.session = payload.user; state.csrf = payload.csrfToken;
  } catch (error) {
    if (String(error.message).includes("temporarily")) toast(error.message);
    showLanding();
    return;
  }
  showApp();
  try { await loadConversations(); }
  catch (error) { showEmpty(); toast(error.message); }
}

async function loadBrand() {
  try {
    const payload = await api("/api/brand");
    const brand = payload.brand || {};
    const name = brand.name || "Kepler";
    const avatar = brand.avatarUrl || "/assets/avatar.png";
    state.brand = { name, avatarUrl: avatar };
    document.title = name;
    document.querySelectorAll("[data-brand-name]").forEach((node) => { node.textContent = name; });
    document.querySelectorAll("[data-brand-avatar]").forEach((node) => { node.src = avatar; });
    document.querySelectorAll("[data-brand-placeholder]").forEach((node) => { node.placeholder = `Ask ${name} anything…`; });
    document.querySelectorAll("[data-brand-aria]").forEach((node) => { node.setAttribute("aria-label", `Message ${name}`); });
    document.querySelectorAll("[data-brand-note]").forEach((node) => { node.textContent = `${name} can make mistakes. Check important decisions.`; });
    document.documentElement.style.setProperty("--brand-avatar", `url("${avatar.replace(/["\\]/g, "")}")`);
  } catch (_) {
    // The static defaults remain usable when branding cannot be loaded.
  }
}

function showLanding() { $("#landing").classList.remove("hidden"); $("#app").classList.add("hidden"); }
function showApp() {
  $("#landing").classList.add("hidden"); $("#app").classList.remove("hidden");
  const user = state.session;
  $("#profile-name").textContent = user.displayName || user.email || "User";
  const avatar = $("#profile-avatar");
  if (user.avatarUrl) avatar.style.backgroundImage = `url("${user.avatarUrl.replace(/["\\]/g, "")}")`;
  else avatar.textContent = (user.displayName || "K").slice(0, 1).toUpperCase();
}

function showAuthError() {
  const code = new URLSearchParams(location.search).get("auth_error");
  if (!code) return;
  const messages = { not_allowed: "Your Slack account does not have access.", access_denied: "Slack sign in was cancelled.", expired_state: "This sign-in link expired. Try again.", provider_error: "Slack could not verify your account. Try again." };
  const node = $("#auth-error"); node.textContent = messages[code] || "Sign in could not be completed. Please try again."; node.classList.remove("hidden");
  history.replaceState({}, "", "/");
}

async function loadConversations(selectFirst = true) {
  const payload = await api("/api/conversations"); state.conversations = payload.conversations || []; renderConversations();
  if (selectFirst && !state.current && state.conversations.length) await selectConversation(state.conversations[0].id);
  if (!state.conversations.length) showEmpty();
}

function renderConversations() {
  const list = $("#conversation-list"); list.replaceChildren();
  if (!state.conversations.length) { const empty = document.createElement("p"); empty.className = "history-empty"; empty.textContent = "Your conversations will appear here."; list.append(empty); return; }
  for (const conversation of state.conversations) {
    const button = document.createElement("button"); button.className = `conversation-item${state.current?.id === conversation.id ? " active" : ""}`; button.dataset.id = conversation.id; button.setAttribute("role", "listitem");
    button.innerHTML = `<span class="chat-glyph">⌁</span><span class="item-copy"><strong></strong><small></small></span><span class="item-menu">•••</span>`;
    button.querySelector("strong").textContent = conversation.title; button.querySelector("small").textContent = relativeTime(conversation.updatedAt);
    button.addEventListener("click", () => selectConversation(conversation.id)); list.append(button);
  }
}

async function createConversation() {
  const payload = await api("/api/conversations", { method: "POST", body: "{}" });
  state.conversations.unshift(payload.conversation); renderConversations(); await selectConversation(payload.conversation.id); closeSidebar();
  $("#message-input").focus(); return payload.conversation;
}

async function selectConversation(id) {
  if (state.current?.id === id) { closeSidebar(); return; }
  closeStream(); state.current = state.conversations.find((item) => item.id === id) || null; state.events = []; state.maxSequence = 0; state.running = false;
  renderConversations(); $("#conversation-title").textContent = state.current?.title || "New conversation";
  const payload = await api(`/api/conversations/${encodeURIComponent(id)}/messages`); state.events = payload.events || [];
  for (const event of state.events) state.maxSequence = Math.max(state.maxSequence, event.sequence || 0);
  renderTimeline(); openStream(); closeSidebar();
}

function openStream() {
  if (!state.current) return;
  const id = state.current.id; const stream = new EventSource(`/api/conversations/${encodeURIComponent(id)}/events?after=${state.maxSequence}`);
  state.stream = stream;
  stream.addEventListener("kepler", (message) => { if (state.current?.id !== id) return; const event = JSON.parse(message.data); receiveEvent(event); });
  stream.onerror = () => { if (state.stream === stream && stream.readyState === EventSource.CLOSED) setTimeout(openStream, 1200); };
}
function closeStream() { if (state.stream) state.stream.close(); state.stream = null; }

function receiveEvent(event) {
  if (event.sequence && state.events.some((item) => item.id === event.id)) return;
  state.maxSequence = Math.max(state.maxSequence, event.sequence || 0);
  if (event.kind === "assistant_delta") {
    let live = state.events.find((item) => item.kind === "assistant_delta" && item.turnId === event.turnId);
    if (live) live.text += event.text; else state.events.push({ ...event });
  } else if (event.kind === "message" && event.role === "assistant") {
    state.events = state.events.filter((item) => !(item.kind === "assistant_delta" && item.turnId === event.turnId)); state.events.push(event);
  } else if (event.kind === "message" && event.role === "user") {
    const optimistic = state.events.findIndex((item) => item.kind === "message" && item.role === "user" && item.optimistic && item.text === event.text);
    if (optimistic >= 0) state.events.splice(optimistic, 1); state.events.push(event);
  } else if (event.kind === "tool" || event.kind === "approval") {
    const index = state.events.findIndex((item) => item.kind === event.kind && item.toolCallId === event.toolCallId);
    if (index >= 0) state.events[index] = event; else state.events.push(event);
  } else state.events.push(event);
  if (event.kind === "turn") {
    state.running = event.status === "running";
    if (["completed", "canceled", "failed", "pending_approval", "pending_input", "max_steps", "output_limit"].includes(event.status)) state.running = false;
    updateComposer();
  }
  renderTimeline();
}

function renderTimeline() {
  const visible = state.events.filter((event) => ["message", "assistant_delta", "tool", "approval", "turn"].includes(event.kind));
  if (!visible.some((event) => event.kind === "message" || event.kind === "assistant_delta")) { showEmpty(); return; }
  $("#empty-state").classList.add("hidden"); const timeline = $("#timeline"); timeline.classList.remove("hidden"); timeline.replaceChildren();
  let activity = null;
  for (const event of visible) {
    if (event.kind === "message" || event.kind === "assistant_delta") {
      activity = null; timeline.append(renderMessage(event));
    } else if (event.kind === "tool") {
      if (!activity) { activity = document.createElement("div"); activity.className = "activity-stack"; timeline.append(activity); }
      activity.append(renderTool(event));
    } else if (event.kind === "approval" && event.status === "pending") {
      activity = null; timeline.append(renderApproval(event));
    } else if (event.kind === "turn" && event.status === "failed") {
      const error = document.createElement("p"); error.className = "turn-error"; error.textContent = event.text || `${state.brand.name} could not complete this turn.`; timeline.append(error);
    }
  }
  requestAnimationFrame(() => { timeline.scrollTop = timeline.scrollHeight; }); updateComposer();
}

function renderMessage(event) {
  const row = document.createElement("article"); row.className = `message-row ${event.role}`;
  const avatar = document.createElement("div"); avatar.className = "message-avatar"; avatar.textContent = event.role === "assistant" ? "" : "YOU"; avatar.setAttribute("aria-label", event.role === "assistant" ? state.brand.name : "You");
  const content = document.createElement("div"); content.className = `message-content${event.kind === "assistant_delta" ? " streaming-cursor" : ""}`;
  const label = document.createElement("span"); label.className = "message-label"; label.textContent = event.role === "assistant" ? state.brand.name : "You"; content.append(label);
  const body = document.createElement("div"); body.className = "markdown"; body.innerHTML = markdown(event.text || ""); content.append(body); row.append(avatar, content); return row;
}

function renderTool(event) {
  const row = document.createElement("div"); row.className = `tool-row ${event.status}`; const dot = document.createElement("i"); const copy = document.createElement("span");
  copy.textContent = `${event.status === "running" ? "Running" : event.status === "failed" ? "Could not complete" : "Completed"} · ${friendlyTool(event.tool)}`; row.append(dot, copy); return row;
}

function renderApproval(event) {
  const card = document.createElement("section"); card.className = "approval-card"; card.innerHTML = `<strong></strong><p>This action changes data or an external service and needs your confirmation.</p><div><button class="deny">Deny</button><button class="approve">Approve once</button></div>`;
  card.querySelector("strong").textContent = `${friendlyTool(event.tool)} needs approval`;
  card.querySelector(".deny").addEventListener("click", () => resolveApproval(event, false)); card.querySelector(".approve").addEventListener("click", () => resolveApproval(event, true)); return card;
}

async function resolveApproval(event, approved) {
  try { state.running = true; updateComposer(); await api(`/api/conversations/${encodeURIComponent(state.current.id)}/approvals`, { method: "POST", body: JSON.stringify({ requestId: requestID(), turnId: event.turnId, toolCallId: event.toolCallId, approved }) }); }
  catch (error) { state.running = false; updateComposer(); toast(error.message); }
}

function showEmpty() { $("#empty-state").classList.remove("hidden"); $("#timeline").classList.add("hidden"); $("#conversation-title").textContent = state.current?.title || "New conversation"; updateComposer(); }

async function sendMessage(text) {
  text = text.trim(); if (!text || state.running) return;
  try {
    if (!state.current) await createConversation();
    state.events.push({ id: `optimistic-${Date.now()}`, kind: "message", role: "user", text, optimistic: true }); state.running = true; renderTimeline();
    await api(`/api/conversations/${encodeURIComponent(state.current.id)}/turns`, { method: "POST", body: JSON.stringify({ requestId: requestID(), message: text }) });
    $("#message-input").value = ""; resizeInput(); setTimeout(() => loadConversations(false), 500);
  } catch (error) { state.events = state.events.filter((item) => !item.optimistic); state.running = false; renderTimeline(); toast(error.message); }
}

function updateComposer() { $("#send-message").classList.toggle("hidden", state.running); $("#stop-turn").classList.toggle("hidden", !state.running); $("#message-input").disabled = state.running; $("#input-hint").textContent = state.running ? `${state.brand.name} is working` : "Enter to send"; }
function requestID() { const bytes = crypto.getRandomValues(new Uint8Array(18)); return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join(""); }
function friendlyTool(name = "tool") { return name.replace(/[-_]/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase()); }
function relativeTime(raw) { const date = new Date(raw); const seconds = Math.max(0, (Date.now() - date.getTime()) / 1000); if (seconds < 60) return "Just now"; if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`; if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`; return date.toLocaleDateString(undefined, { month: "short", day: "numeric" }); }

function markdown(text) {
  const escaped = escapeHTML(text); const blocks = escaped.split(/```/); let output = "";
  blocks.forEach((block, index) => { if (index % 2) { const code = block.replace(/^[\w+-]+\n/, ""); output += `<pre><code>${code}</code></pre>`; } else { output += block.split(/\n{2,}/).filter(Boolean).map((paragraph) => `<p>${inlineMarkdown(paragraph).replace(/\n/g, "<br>")}</p>`).join(""); } }); return output || "<p></p>";
}
function inlineMarkdown(text) { return text.replace(/`([^`]+)`/g, "<code>$1</code>").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>").replace(/\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/g, '<a href="$2" target="_blank" rel="noopener noreferrer">$1</a>'); }
function escapeHTML(text) { return String(text).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#039;" }[char])); }
function resizeInput() { const input = $("#message-input"); input.style.height = "auto"; input.style.height = `${Math.min(input.scrollHeight, 150)}px`; }
function toast(message) { const node = $("#toast"); node.textContent = message; node.classList.add("show"); clearTimeout(toast.timer); toast.timer = setTimeout(() => node.classList.remove("show"), 3200); }
function openSidebar() { $("#sidebar").classList.add("open"); $("#sidebar-scrim").classList.remove("hidden"); }
function closeSidebar() { $("#sidebar").classList.remove("open"); $("#sidebar-scrim").classList.add("hidden"); }

$("#composer").addEventListener("submit", (event) => { event.preventDefault(); const input = $("#message-input"); sendMessage(input.value); });
$("#message-input").addEventListener("input", resizeInput);
$("#message-input").addEventListener("keydown", (event) => { if (event.key === "Enter" && !event.shiftKey && !event.isComposing) { event.preventDefault(); sendMessage(event.currentTarget.value); } });
$("#new-chat").addEventListener("click", createConversation); $("#refresh-list").addEventListener("click", () => loadConversations(false));
$("#stop-turn").addEventListener("click", async () => { try { await api(`/api/conversations/${encodeURIComponent(state.current.id)}/turns/stop`, { method: "POST", body: "{}" }); } catch (error) { toast(error.message); } });
$("#profile-button").addEventListener("click", () => $("#profile-menu").classList.toggle("hidden"));
$("#logout").addEventListener("click", async () => { await api("/api/logout", { method: "POST", body: "{}" }); location.assign("/"); });
$("#open-sidebar").addEventListener("click", openSidebar); $("#close-sidebar").addEventListener("click", closeSidebar); $("#sidebar-scrim").addEventListener("click", closeSidebar);
document.querySelectorAll("[data-prompt]").forEach((button) => button.addEventListener("click", () => sendMessage(button.dataset.prompt)));
$("#rename-chat").addEventListener("click", () => { if (!state.current) return; $("#rename-input").value = state.current.title; $("#rename-dialog").showModal(); setTimeout(() => $("#rename-input").select(), 30); });
$("#rename-form").addEventListener("submit", async (event) => { if (event.submitter?.value === "cancel") return; event.preventDefault(); try { const payload = await api(`/api/conversations/${encodeURIComponent(state.current.id)}`, { method: "PATCH", body: JSON.stringify({ title: $("#rename-input").value }) }); state.current = payload.conversation; const index = state.conversations.findIndex((item) => item.id === state.current.id); state.conversations[index] = state.current; renderConversations(); $("#conversation-title").textContent = state.current.title; $("#rename-dialog").close(); } catch (error) { toast(error.message); } });
$("#archive-chat").addEventListener("click", async () => { if (!state.current || !confirm("Archive this conversation?")) return; try { await api(`/api/conversations/${encodeURIComponent(state.current.id)}`, { method: "PATCH", body: JSON.stringify({ archived: true }) }); closeStream(); state.current = null; state.events = []; await loadConversations(); } catch (error) { toast(error.message); } });
document.addEventListener("keydown", (event) => { if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") { event.preventDefault(); createConversation(); } });
window.addEventListener("beforeunload", closeStream);

boot();
