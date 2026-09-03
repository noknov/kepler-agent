import { $, state } from "./state.js";
import { renderMarkdown } from "./markdown.js";
import {
  activityStats,
  ensureActivityBlock,
  normalizeEvents,
  patchActivityBlock,
  refreshActivityBlocks,
  startActivityTimer,
  stopActivityTimer,
  updateActivityBlock,
} from "./activity.js";

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
    state.session = payload.user;
    state.csrf = payload.csrfToken;
  } catch (error) {
    if (String(error.message).includes("temporarily")) toast(error.message);
    showLanding();
    return;
  }
  showApp();
  try {
    await loadConversations();
  } catch (error) {
    showEmpty();
    toast(error.message);
  }
}

async function loadBrand() {
  try {
    const payload = await api("/api/brand");
    const brand = payload.brand || {};
    state.brand = { name: brand.name || "Kepler" };
    applyBrand();
  } catch (_) {
    applyBrand();
  }
}

function applyBrand() {
  document.title = state.brand.name;
  document.querySelectorAll("[data-brand-name]").forEach((node) => {
    node.textContent = state.brand.name;
  });
}

function showLanding() {
  $("#landing").classList.remove("hidden");
  $("#app").classList.add("hidden");
}

function showApp() {
  $("#landing").classList.add("hidden");
  $("#app").classList.remove("hidden");
  const user = state.session;
  $("#profile-name").textContent = user.displayName || user.email || "User";
  const avatar = $("#profile-avatar");
  if (user.avatarUrl) {
    avatar.style.backgroundImage = `url("${user.avatarUrl.replace(/["\\]/g, "")}")`;
    avatar.textContent = "";
  } else {
    avatar.style.backgroundImage = "";
    avatar.textContent = (user.displayName || "U").slice(0, 1).toUpperCase();
  }
}

function showAuthError() {
  const code = new URLSearchParams(location.search).get("auth_error");
  if (!code) return;
  const messages = {
    not_allowed: "Your Slack account does not have access.",
    access_denied: "Slack sign in was cancelled.",
    expired_state: "This sign-in link expired. Try again.",
    provider_error: "Slack could not verify your account. Try again.",
  };
  const node = $("#auth-error");
  node.textContent = messages[code] || "Sign in could not be completed. Please try again.";
  node.classList.remove("hidden");
  history.replaceState({}, "", "/");
}

async function loadConversations(selectFirst = true) {
  const payload = await api("/api/conversations?limit=50&offset=0");
  state.conversations = payload.conversations || [];
  state.conversationOffset = state.conversations.length;
  state.hasMoreConversations = Boolean(payload.hasMore);
  renderConversations();
  if (selectFirst && !state.current && state.conversations.length) {
    await selectConversation(state.conversations[0].id);
  }
  if (!state.conversations.length) showEmpty();
}

async function loadMoreConversations() {
  if (state.loadingMoreConversations || !state.hasMoreConversations) return;
  state.loadingMoreConversations = true;
  const button = $("#load-more-conversations");
  button.textContent = "Loading…";
  try {
    const payload = await api(`/api/conversations?limit=50&offset=${state.conversationOffset}`);
    const next = payload.conversations || [];
    const existing = new Set(state.conversations.map((item) => item.id));
    state.conversations.push(...next.filter((item) => !existing.has(item.id)));
    state.conversationOffset = state.conversations.length;
    state.hasMoreConversations = Boolean(payload.hasMore);
    renderConversations();
  } catch (error) {
    toast(error.message);
  } finally {
    state.loadingMoreConversations = false;
    button.textContent = "Load older conversations";
  }
}

function renderConversations() {
  const list = $("#conversation-list");
	$("#load-more-conversations").classList.toggle("hidden", !state.hasMoreConversations);
  list.replaceChildren();
  if (!state.conversations.length) {
    const empty = document.createElement("p");
    empty.className = "history-empty";
    empty.textContent = "No conversations yet.";
    list.append(empty);
    return;
  }
  for (const conversation of state.conversations) {
    const item = document.createElement("div");
    item.className = `conversation-item${state.current?.id === conversation.id ? " active" : ""}`;
    item.setAttribute("role", "listitem");

    const open = document.createElement("button");
    open.className = "conversation-open";
    open.type = "button";
    open.setAttribute("aria-label", `Open ${conversation.title}`);

    const title = document.createElement("span");
    title.className = "item-title";
    title.textContent = conversation.title;
    open.append(title);

    const menu = document.createElement("button");
    menu.className = "item-menu";
    menu.type = "button";
    menu.setAttribute("aria-label", "Conversation options");
    menu.textContent = "•••";
    menu.addEventListener("click", (event) => {
      openContextMenu(event.currentTarget, conversation);
    });
    open.addEventListener("click", () => selectConversation(conversation.id));
    item.append(open, menu);

    list.append(item);
  }
}

async function createConversation() {
  if (state.current && !conversationHasMessages(state.events)) {
    showEmpty();
    closeSidebar();
    $("#message-input").focus();
    return state.current;
  }

  const existing = findUnusedConversation();
  if (existing) {
    if (state.current?.id !== existing.id) await selectConversation(existing.id);
    showEmpty();
    closeSidebar();
    $("#message-input").focus();
    return existing;
  }

  const payload = await api("/api/conversations", { method: "POST", body: "{}" });
  state.conversations.unshift(payload.conversation);
  renderConversations();
  await selectConversation(payload.conversation.id);
  closeSidebar();
  $("#message-input").focus();
  return payload.conversation;
}

function conversationHasMessages(events) {
  return (events || []).some((event) => event.kind === "message" || event.kind === "assistant_delta");
}

function findUnusedConversation() {
  for (const conversation of state.conversations) {
    if (conversation.title !== "New conversation") continue;
    if (conversation.hasMessages) continue;
    return conversation;
  }
  return null;
}

async function selectConversation(id) {
  if (state.current?.id === id && state.currentLoaded) {
    closeSidebar();
    return;
  }
  closeStream();
  clearTimeline();
  state.current = state.conversations.find((item) => item.id === id) || null;
  state.currentLoaded = false;
  state.events = [];
  state.maxSequence = 0;
  state.running = false;
  renderConversations();
  const payload = await api(`/api/conversations/${encodeURIComponent(id)}/messages`);
  state.events = normalizeEvents(payload.events || []);
  for (const event of state.events) {
    state.maxSequence = Math.max(state.maxSequence, event.sequence || 0);
  }
  state.currentLoaded = true;
  renderTimeline(true);
  openStream();
  closeSidebar();
}

function showEmpty() {
  $("#empty-state").classList.remove("hidden");
  $("#timeline").classList.add("hidden");
  updateComposer();
}

function canReconnectStream() {
  if (!state.current) return false;
  if (document.visibilityState === "hidden") return false;
  return state.running || Date.now() < state.streamGraceUntil;
}

function scheduleStreamReconnect() {
  if (state.streamReconnectTimer || !canReconnectStream()) return;
  state.streamReconnectTimer = window.setTimeout(() => {
    state.streamReconnectTimer = null;
    if (canReconnectStream()) openStream();
  }, 2000);
}

function openStream() {
  if (!state.current) return;
  if (state.streamReconnectTimer) {
    clearTimeout(state.streamReconnectTimer);
    state.streamReconnectTimer = null;
  }
  const id = state.current.id;
  const stream = new EventSource(`/api/conversations/${encodeURIComponent(id)}/events?after=${state.maxSequence}`);
  state.stream = stream;
  stream.addEventListener("kepler", (message) => {
    if (state.stream !== stream || state.current?.id !== id) return;
    receiveEvent(JSON.parse(message.data));
  });
  stream.onerror = () => {
    if (state.stream !== stream) return;
    closeStream();
    scheduleStreamReconnect();
  };
}

function closeStream() {
  if (state.streamReconnectTimer) {
    clearTimeout(state.streamReconnectTimer);
    state.streamReconnectTimer = null;
  }
  if (state.stream) state.stream.close();
  state.stream = null;
}

function receiveEvent(event) {
  if (event.id && state.events.some((item) => item.id === event.id)) return;
  state.maxSequence = Math.max(state.maxSequence, event.sequence || 0);

  if (
    state.pendingThinking &&
    (event.kind === "assistant_delta" ||
      event.kind === "tool" ||
      event.kind === "plan" ||
      (event.kind === "message" && event.role === "assistant"))
  ) {
    state.pendingThinking = false;
    const pendingKey = "activity:pending";
    state.timelineNodes.get(pendingKey)?.remove();
    state.timelineNodes.delete(pendingKey);
  }

  if (event.kind === "assistant_delta") {
    // A completed message is authoritative. A delayed SSE delta must never
    // recreate the streaming row (and its cursor) after that final message.
    if (
      state.events.some(
        (item) => item.kind === "message" && item.role === "assistant" && item.turnId === event.turnId,
      )
    ) {
      return;
    }
    let live = state.events.find((item) => item.kind === "assistant_delta" && item.turnId === event.turnId);
    if (live) live.text = event.replace ? event.text : live.text + event.text;
    else state.events.push({ ...event });
    updateStreamingMessage(event.turnId);
    return;
  }

  if (event.kind === "tool") {
    const index = state.events.findIndex((item) => item.kind === event.kind && item.toolCallId === event.toolCallId);
    if (index >= 0) state.events[index] = event;
    else state.events.push(event);
    if (!state.activityStart.has(event.turnId)) {
      state.activityStart.set(event.turnId, event.at ? Date.parse(event.at) : Date.now());
    }
    patchActivityBlock(event.turnId, scheduleRender);
    return;
  }

  if (event.kind === "plan") {
    state.events.push(event);
    if (!state.activityStart.has(event.turnId)) {
      state.activityStart.set(event.turnId, event.at ? Date.parse(event.at) : Date.now());
    }
    patchActivityBlock(event.turnId, scheduleRender);
    return;
  }

  if (event.kind === "message" && event.role === "assistant") {
    state.events = state.events.filter((item) => !(item.kind === "assistant_delta" && item.turnId === event.turnId));
    state.events.push(event);
    const deltaKey = `delta:${event.turnId}`;
    // Remove the existing live node before forgetting it. Leaving it in the
    // DOM was what caused the final response to be rendered a second time.
    state.timelineNodes.get(deltaKey)?.remove();
    state.timelineNodes.delete(deltaKey);
    state.markdownCache.delete(deltaKey);
    const pendingRender = state.streamRenderTimers.get(event.turnId);
    if (pendingRender) clearTimeout(pendingRender);
    state.streamRenderTimers.delete(event.turnId);
    stopActivityTimer();
    refreshActivityBlocks();
  } else if (event.kind === "message" && event.role === "user") {
    const optimistic = state.events.findIndex(
      (item) => item.kind === "message" && item.role === "user" && item.optimistic && item.text === event.text,
    );
    if (optimistic >= 0) state.events.splice(optimistic, 1);
    state.events.push(event);
    const conversation = state.conversations.find((item) => item.id === state.current?.id);
    if (conversation) conversation.hasMessages = true;
  } else if (event.kind === "approval") {
    const index = state.events.findIndex((item) => item.kind === event.kind && item.toolCallId === event.toolCallId);
    if (index >= 0) state.events[index] = event;
    else state.events.push(event);
  } else {
    state.events.push(event);
  }

  if (event.kind === "turn") {
    state.running = event.status === "running";
    if (["completed", "canceled", "failed", "pending_approval", "pending_input", "max_steps", "output_limit"].includes(event.status)) {
      state.running = false;
      state.streamGraceUntil = Date.now() + 5000;
      stopActivityTimer();
      refreshActivityBlocks();
      if (!state.running) scheduleStreamReconnect();
    }
    updateComposer();
  }
  scheduleRender();
}

function eventKey(event) {
  if (event.kind === "assistant_delta") return `delta:${event.turnId}`;
  if (event.id) return event.id;
  return `${event.kind}:${event.turnId || event.toolCallId || Math.random()}`;
}

function clearTimeline() {
  state.timelineNodes.clear();
  state.markdownCache.clear();
  for (const timer of state.streamRenderTimers.values()) clearTimeout(timer);
  state.streamRenderTimers.clear();
  state.activityStart.clear();
  state.pendingThinking = false;
  state.streamGraceUntil = 0;
  stopActivityTimer();
  const timeline = $("#timeline");
  timeline.replaceChildren();
}

function scheduleRender() {
  if (state.renderScheduled) return;
  state.renderScheduled = true;
  requestAnimationFrame(() => {
    state.renderScheduled = false;
    renderTimeline();
  });
}

function renderTimeline(force = false) {
  const visible = state.events.filter((event) =>
    ["message", "assistant_delta", "tool", "plan", "approval", "turn"].includes(event.kind),
  );
  const hasMessages = visible.some((event) => event.kind === "message" || event.kind === "assistant_delta");

  if (!hasMessages) {
    showEmpty();
    return;
  }

  $("#empty-state").classList.add("hidden");
  const timeline = $("#timeline");
  timeline.classList.remove("hidden");

  const seenKeys = new Set();
  const orderedNodes = [];
  const placeNode = (key) => {
    const node = state.timelineNodes.get(key);
    if (node && !orderedNodes.includes(node)) orderedNodes.push(node);
  };
  const turnsWithAssistantOutput = new Set(
    visible
      .filter(
        (event) =>
          event.turnId &&
          (event.kind === "assistant_delta" || (event.kind === "message" && event.role === "assistant")),
      )
      .map((event) => event.turnId),
  );

  for (const event of visible) {
    const key = eventKey(event);
    seenKeys.add(key);

    if (event.kind === "message" || event.kind === "assistant_delta") {
      if (state.timelineNodes.has(key)) {
        if (force || event.kind === "message") {
          updateMessageContent(state.timelineNodes.get(key), event);
        }
        placeNode(key);
        continue;
      }
      const node = renderMessage(event);
      state.timelineNodes.set(key, node);
      timeline.append(node);
      placeNode(key);
    } else if (event.kind === "tool" || event.kind === "plan") {
      // Thinking is pre-answer feedback only. Once response text is visible,
      // completed or late-arriving tool events must not leave it behind.
      if (turnsWithAssistantOutput.has(event.turnId)) continue;
      ensureActivityBlock(event.turnId, timeline, seenKeys);
      updateActivityBlock(event.turnId);
      if (activityStats(event.turnId)?.running) startActivityTimer();
      placeNode(`activity:${event.turnId}`);
    } else if (event.kind === "approval" && event.status === "pending") {
      const apprKey = `approval:${event.toolCallId}`;
      seenKeys.add(apprKey);
      if (state.timelineNodes.has(apprKey)) {
        placeNode(apprKey);
        continue;
      }
      const card = renderApproval(event);
      state.timelineNodes.set(apprKey, card);
      timeline.append(card);
      placeNode(apprKey);
    } else if (event.kind === "turn" && event.status === "failed") {
      const errKey = `error:${event.turnId}`;
      seenKeys.add(errKey);
      if (state.timelineNodes.has(errKey)) {
        placeNode(errKey);
        continue;
      }
      const error = document.createElement("p");
      error.className = "turn-error";
      error.textContent = event.text || "Could not complete this turn.";
      state.timelineNodes.set(errKey, error);
      timeline.append(error);
      placeNode(errKey);
    }
  }

  if (state.pendingThinking) {
    ensureActivityBlock("pending", timeline, seenKeys);
    placeNode("activity:pending");
  }

  for (const [key, node] of state.timelineNodes) {
    if (!seenKeys.has(key)) {
      node.remove();
      state.timelineNodes.delete(key);
      state.markdownCache.delete(key);
    }
  }

  // Nodes are updated in place for smooth streaming, but their position must
  // still follow the canonical event order when an optimistic user message is
  // replaced by the persisted one.
  for (const node of orderedNodes) timeline.append(node);

  scrollTimelineToEnd();
  updateComposer();
}

function updateStreamingMessage(turnId) {
  const key = `delta:${turnId}`;
  const event = state.events.find((item) => item.kind === "assistant_delta" && item.turnId === turnId);
  if (!event) return;

  let node = state.timelineNodes.get(key);
  if (!node) {
    scheduleRender();
    return;
  }

  if (state.streamRenderTimers.has(turnId)) return;
  state.streamRenderTimers.set(
    turnId,
    setTimeout(() => {
      state.streamRenderTimers.delete(turnId);
      const latest = state.events.find((item) => item.kind === "assistant_delta" && item.turnId === turnId);
      if (!latest) return;
      updateMessageContent(node, latest, true);
      scrollTimelineToEnd();
    }, 80),
  );
}

function scrollTimelineToEnd() {
  if (state.streamRenderFrame) return;
  state.streamRenderFrame = requestAnimationFrame(() => {
    state.streamRenderFrame = null;
    const timeline = $("#timeline");
    const nearBottom = timeline.scrollHeight - timeline.scrollTop - timeline.clientHeight < 96;
    if (nearBottom || state.running) timeline.scrollTop = timeline.scrollHeight;
  });
}

function renderMessage(event) {
  const row = document.createElement("article");
  row.className = `message-row ${event.role}`;
  const content = document.createElement("div");
  content.className = `message-content${event.kind === "assistant_delta" ? " streaming-cursor" : ""}`;
  const body = document.createElement("div");
  body.className = "markdown";
  content.append(body);
  row.append(content);
  updateMessageContent(row, event);
  return row;
}

function updateMessageContent(row, event, streaming = false) {
  const content = row.querySelector(".message-content");
  const body = row.querySelector(".markdown");
  if (!content || !body) return;

  content.classList.toggle("streaming-cursor", event.kind === "assistant_delta");

  const key = eventKey(event);
  const text = event.text || "";

  if (event.role === "user") {
    body.textContent = text;
    return;
  }

  if (streaming) {
    const cached = state.markdownCache.get(key);
    if (cached && cached.source === text) return;
    const html = renderMarkdown(text);
    state.markdownCache.set(key, { source: text, html });
    body.innerHTML = html;
    return;
  }

  const html = renderMarkdown(text);
  state.markdownCache.set(key, { source: text, html });
  body.innerHTML = html;
}

function renderApproval(event) {
  const card = document.createElement("section");
  card.className = "approval-card";
  card.innerHTML = `<strong></strong><p>This action needs your confirmation before proceeding.</p><div><button class="deny" type="button">Deny</button><button class="approve" type="button">Approve</button></div>`;
  card.querySelector("strong").textContent = `${friendlyTool(event.tool)} needs approval`;
  card.querySelector(".deny").addEventListener("click", () => resolveApproval(event, false));
  card.querySelector(".approve").addEventListener("click", () => resolveApproval(event, true));
  return card;
}

async function resolveApproval(event, approved) {
  try {
    state.running = true;
    updateComposer();
    await api(`/api/conversations/${encodeURIComponent(state.current.id)}/approvals`, {
      method: "POST",
      body: JSON.stringify({
        requestId: requestID(),
        turnId: event.turnId,
        toolCallId: event.toolCallId,
        approved,
      }),
    });
  } catch (error) {
    state.running = false;
    updateComposer();
    toast(error.message);
  }
}

async function sendMessage(text) {
  text = text.trim();
  if (!text || state.running) return;
  const input = $("#message-input");
  try {
    if (!state.current) await createConversation();
    input.value = "";
    resizeInput();
    state.events.push({
      id: `optimistic-${Date.now()}`,
      kind: "message",
      role: "user",
      text,
      optimistic: true,
    });
    state.running = true;
    // Show feedback at the point of intent, instead of waiting for the worker
    // to acknowledge the turn over SSE.
    state.pendingThinking = true;
    updateComposer();
    scheduleRender();
    await api(`/api/conversations/${encodeURIComponent(state.current.id)}/turns`, {
      method: "POST",
      body: JSON.stringify({ requestId: requestID(), message: text }),
    });
    const conversation = state.conversations.find((item) => item.id === state.current?.id);
    if (conversation) conversation.hasMessages = true;
    window.setTimeout(() => loadConversations(false), 500);
  } catch (error) {
    state.events = state.events.filter((item) => !item.optimistic);
    state.running = false;
    state.pendingThinking = false;
    updateComposer();
    scheduleRender();
    toast(error.message);
  }
}

function updateComposer() {
  $("#send-message").classList.toggle("hidden", state.running);
  $("#stop-turn").classList.toggle("hidden", !state.running);
  $("#input-hint").textContent = state.running ? "Working…" : "Enter to send";
}

function requestID() {
  const bytes = crypto.getRandomValues(new Uint8Array(18));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function friendlyTool(name = "tool") {
  return name.replace(/[-_]/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}

function openContextMenu(anchor, conversation) {
  state.contextTarget = conversation;
  const menu = $("#context-menu");
  const rect = anchor.getBoundingClientRect();
  menu.classList.remove("hidden");
  menu.style.top = `${rect.bottom + 4}px`;
  menu.style.left = `${Math.min(rect.left, window.innerWidth - 180)}px`;
  anchor.closest(".conversation-item")?.classList.add("menu-open");
}

function closeContextMenu() {
  $("#context-menu").classList.add("hidden");
  state.contextTarget = null;
  document.querySelectorAll(".conversation-item.menu-open").forEach((item) => item.classList.remove("menu-open"));
}

function openRenameDialog(conversation) {
  state.contextTarget = conversation || state.current;
  if (!state.contextTarget) return;
  $("#rename-input").value = state.contextTarget.title;
  $("#rename-dialog").showModal();
  setTimeout(() => $("#rename-input").select(), 30);
}

async function archiveConversation(conversation) {
  const target = conversation || state.current;
  if (!target || !confirm("Archive this conversation?")) return;
  try {
    await api(`/api/conversations/${encodeURIComponent(target.id)}`, {
      method: "PATCH",
      body: JSON.stringify({ archived: true }),
    });
    if (state.current?.id === target.id) {
      closeStream();
      clearTimeline();
      state.current = null;
      state.events = [];
    }
    await loadConversations();
    closeContextMenu();
  } catch (error) {
    toast(error.message);
  }
}

function resizeInput() {
  if (state.resizeFrame) return;
  state.resizeFrame = requestAnimationFrame(() => {
    state.resizeFrame = null;
    const input = $("#message-input");
    input.style.height = "auto";
    input.style.height = `${Math.min(input.scrollHeight, 200)}px`;
  });
}

function toast(message) {
  const node = $("#toast");
  node.textContent = message;
  node.classList.add("show");
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => node.classList.remove("show"), 3200);
}

function openSidebar() {
  $("#sidebar").classList.add("open");
  $("#sidebar-scrim").classList.remove("hidden");
}

function closeSidebar() {
  $("#sidebar").classList.remove("open");
  $("#sidebar-scrim").classList.add("hidden");
}

$("#composer").addEventListener("submit", (event) => {
  event.preventDefault();
  sendMessage($("#message-input").value);
});

$("#message-input").addEventListener("input", resizeInput);
$("#message-input").addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    sendMessage(event.currentTarget.value);
  }
});

$("#new-chat").addEventListener("click", createConversation);
$("#load-more-conversations").addEventListener("click", loadMoreConversations);
$("#stop-turn").addEventListener("click", async () => {
  try {
    await api(`/api/conversations/${encodeURIComponent(state.current.id)}/turns/stop`, { method: "POST", body: "{}" });
  } catch (error) {
    toast(error.message);
  }
});

$("#profile-button").addEventListener("click", () => $("#profile-menu").classList.toggle("hidden"));
$("#logout").addEventListener("click", async () => {
  await api("/api/logout", { method: "POST", body: "{}" });
  location.assign("/");
});

$("#open-sidebar").addEventListener("click", openSidebar);
$("#close-sidebar").addEventListener("click", closeSidebar);
$("#sidebar-scrim").addEventListener("click", closeSidebar);

$("#context-menu").addEventListener("click", (event) => {
  const action = event.target.closest("[data-action]")?.dataset.action;
  if (!action) return;
  const target = state.contextTarget;
  closeContextMenu();
  if (action === "rename") openRenameDialog(target);
  else if (action === "archive") archiveConversation(target);
});

document.addEventListener("click", (event) => {
  if (!event.target.closest("#context-menu") && !event.target.closest(".item-menu")) {
    closeContextMenu();
  }
  if (!event.target.closest("#profile-button") && !event.target.closest("#profile-menu")) {
    $("#profile-menu").classList.add("hidden");
  }
});

$("#rename-form").addEventListener("submit", async (event) => {
  if (event.submitter?.value === "cancel") return;
  event.preventDefault();
  const target = state.contextTarget || state.current;
  if (!target) return;
  try {
    const payload = await api(`/api/conversations/${encodeURIComponent(target.id)}`, {
      method: "PATCH",
      body: JSON.stringify({ title: $("#rename-input").value }),
    });
    if (state.current?.id === target.id) state.current = payload.conversation;
    const index = state.conversations.findIndex((item) => item.id === target.id);
    if (index >= 0) state.conversations[index] = payload.conversation;
    renderConversations();
    $("#rename-dialog").close();
    state.contextTarget = null;
  } catch (error) {
    toast(error.message);
  }
});

document.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
    event.preventDefault();
    createConversation();
  }
  if (event.key === "Escape") closeContextMenu();
});

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") {
    closeStream();
    return;
  }
  if (state.current && canReconnectStream()) openStream();
});

window.addEventListener("beforeunload", closeStream);

boot();
