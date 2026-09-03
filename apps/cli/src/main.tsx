#!/usr/bin/env node
import React from "react";
import { render } from "./cc/kepler-ink.js";
import { enableConfigs } from "./cc/utils/config.js";
import { App } from "./App.js";

// Claude Code FullscreenLayout gates on this env var.
process.env.CLAUDE_CODE_NO_FLICKER = "1";

// CC Messages and other vendored components call getGlobalConfig() at render time.
// Must match claude-code entrypoints/cli.tsx — enable before first React paint.
enableConfigs();

if (!process.env.KEPLER_TOKEN || !process.env.KEPLER_API_URL) {
  console.error("KEPLER_TOKEN and KEPLER_API_URL are required (run kepler-agent login)");
  process.exit(1);
}

const cwd = process.env.KEPLER_CWD ?? process.cwd();
const model = process.env.KEPLER_MODEL ?? "kepler";
const user = process.env.KEPLER_USER_ID ?? "";
const sessionId = process.env.KEPLER_SESSION || undefined;
const resume = process.env.KEPLER_RESUME === "1";
const routing = process.env.KEPLER_INPUT_ROUTING === "queue" ? "queue" : "steer";

render(
  <App cwd={cwd} model={model} user={user} sessionId={sessionId} resume={resume} inputRouting={routing} />,
  { exitOnCtrlC: false },
);
