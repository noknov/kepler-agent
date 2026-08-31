export const $ = (selector) => document.querySelector(selector);

export const state = {
  session: null,
  csrf: "",
  brand: {
    name: "Kepler",
  },
  conversations: [],
  current: null,
  events: [],
  stream: null,
  streamReconnectTimer: null,
  streamGraceUntil: 0,
  running: false,
  maxSequence: 0,
  contextTarget: null,
  timelineNodes: new Map(),
  renderScheduled: false,
  markdownCache: new Map(),
  streamRenderTimers: new Map(),
  activityTimer: null,
  activityStart: new Map(),
  resizeFrame: null,
};
