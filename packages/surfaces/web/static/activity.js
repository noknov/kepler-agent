import { state } from "./state.js";

export function createActivityBlock(turnId) {
  const block = document.createElement("div");
  block.className = "activity-card is-active";
  block.dataset.turnId = turnId;
  block.setAttribute("role", "status");
  block.setAttribute("aria-live", "polite");
  const label = document.createElement("span");
  label.className = "activity-label";
  label.textContent = "Thinking";
  block.append(label);
  return block;
}

export function ensureActivityBlock(turnId, timeline, seenKeys) {
  const actKey = `activity:${turnId}`;
  seenKeys.add(actKey);
  if (!state.timelineNodes.has(actKey)) {
    const block = createActivityBlock(turnId);
    state.timelineNodes.set(actKey, block);
    timeline.append(block);
  }
}

export function patchActivityBlock(turnId, scheduleRender) {
  const actKey = `activity:${turnId}`;
  if (state.timelineNodes.has(actKey)) {
    updateActivityBlock(turnId);
    if (activityStats(turnId)?.running) startActivityTimer();
    else if (!hasRunningToolActivity() && !state.running) stopActivityTimer();
    return;
  }
  scheduleRender();
}

export function getLatestPlan(turnId) {
  for (let index = state.events.length - 1; index >= 0; index -= 1) {
    const event = state.events[index];
    if (event.kind === "plan" && event.turnId === turnId && event.plan?.items?.length) {
      return event.plan;
    }
  }
  return null;
}

function planItemClass(status) {
  switch (status) {
    case "completed":
      return "completed";
    case "in_progress":
      return "running";
    case "blocked":
      return "failed";
    default:
      return "pending";
  }
}

function planSummary(plan) {
  const items = plan?.items || [];
  const active = items.find((item) => item.status === "in_progress");
  const completed = items.filter((item) => item.status === "completed").length;
  const blocked = items.filter((item) => item.status === "blocked").length;
  return {
    active,
    completed,
    blocked,
    total: items.length,
    title: (plan.explanation || "").trim(),
  };
}

export function getToolsForTurn(turnId) {
  const byCall = new Map();
  for (const event of state.events) {
    if (event.kind !== "tool" || event.turnId !== turnId || !event.toolCallId) continue;
    const existing = byCall.get(event.toolCallId);
    if (!existing || shouldPreferToolEvent(event, existing)) {
      byCall.set(event.toolCallId, event);
    }
  }
  return Array.from(byCall.values()).sort((left, right) => (left.sequence || 0) - (right.sequence || 0));
}

function toolStatusRank(status) {
  switch (status) {
    case "running":
      return 1;
    case "completed":
      return 2;
    case "failed":
      return 3;
    default:
      return 0;
  }
}

export function shouldPreferToolEvent(next, previous) {
  const nextSequence = next.sequence || 0;
  const previousSequence = previous.sequence || 0;
  if (nextSequence !== previousSequence) return nextSequence > previousSequence;
  return toolStatusRank(next.status) > toolStatusRank(previous.status);
}

export function normalizeEvents(events) {
  const merged = [];
  const toolIndex = new Map();
  for (const event of events) {
    if (event.kind === "tool" && event.toolCallId) {
      const key = `${event.turnId}:${event.toolCallId}`;
      if (toolIndex.has(key)) {
        const index = toolIndex.get(key);
        const existing = merged[index];
        if (shouldPreferToolEvent(event, existing)) merged[index] = event;
      } else {
        toolIndex.set(key, merged.length);
        merged.push(event);
      }
      continue;
    }
    merged.push(event);
  }
  return merged;
}

function isTurnActive(turnId) {
  let turnStatus = null;
  let hasAssistant = false;
  for (const event of state.events) {
    if (event.turnId !== turnId) continue;
    if (event.kind === "turn") turnStatus = event.status;
    if (event.kind === "message" && event.role === "assistant") hasAssistant = true;
  }
  if (hasAssistant) return false;
  if (turnStatus) return turnStatus === "running";
  return state.running && turnId === getActiveTurnId();
}

function getActiveTurnId() {
  for (let index = state.events.length - 1; index >= 0; index -= 1) {
    const turnId = state.events[index].turnId;
    if (turnId) return turnId;
  }
  return null;
}

export function activityStats(turnId) {
  const allTools = getToolsForTurn(turnId);
  const tools = allTools.filter((tool) => tool.tool !== "update_plan");
  const plan = getLatestPlan(turnId);
  if (!tools.length && !plan) return null;

  const times = allTools.map((tool) => (tool.at ? Date.parse(tool.at) : NaN)).filter((value) => !Number.isNaN(value));
  const start = state.activityStart.get(turnId) || (times.length ? Math.min(...times) : Date.now());
  const running = isTurnActive(turnId) && allTools.some((tool) => tool.status === "running");
  const end = running ? Date.now() : times.length ? Math.max(...times) : Date.now();

  return {
    tools,
    plan,
    count: tools.length,
    running,
    failed: tools.filter((tool) => tool.status === "failed").length,
    duration: Math.max(end - start, running ? 0 : 1000),
  };
}

function formatDuration(ms) {
  const totalSeconds = Math.max(1, Math.round(ms / 1000));
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`;
}

export function updateActivityBlock(turnId) {
  const block = state.timelineNodes.get(`activity:${turnId}`);
  const stats = activityStats(turnId);
  if (!block || !stats) return;
  block.classList.toggle("is-active", stats.running);
}

export function refreshActivityBlocks() {
  for (const key of state.timelineNodes.keys()) {
    if (key.startsWith("activity:")) updateActivityBlock(key.slice("activity:".length));
  }
}

export function hasRunningToolActivity() {
  const turnIds = new Set();
  for (const event of state.events) {
    if (event.turnId) turnIds.add(event.turnId);
  }
  for (const turnId of turnIds) {
    if (getToolsForTurn(turnId).some((tool) => tool.status === "running")) return true;
  }
  return false;
}

export function startActivityTimer() {
  if (state.activityTimer) return;
  state.activityTimer = setInterval(() => {
    if (!hasRunningToolActivity() && !state.running && Date.now() >= state.streamGraceUntil) {
      stopActivityTimer();
      refreshActivityBlocks();
      return;
    }
    refreshActivityBlocks();
  }, 1000);
}

export function stopActivityTimer() {
  if (!state.activityTimer) return;
  clearInterval(state.activityTimer);
  state.activityTimer = null;
}

function friendlyTool(name = "tool") {
  return name.replace(/[-_]/g, " ").replace(/\b\w/g, (letter) => letter.toUpperCase());
}
