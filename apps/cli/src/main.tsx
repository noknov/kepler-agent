#!/usr/bin/env node
import React from "react";
import { render } from "ink";
import { AppView } from "./App.js";
import { AlternateScreen } from "./components/AlternateScreen.js";

if (!process.env.KEPLER_TOKEN || !process.env.KEPLER_API_URL) {
  console.error("KEPLER_TOKEN and KEPLER_API_URL are required (run kepler-agent login, then kepler-agent ui)");
  process.exit(1);
}

const cwd = process.env.KEPLER_CWD ?? process.cwd();
const model = process.env.KEPLER_MODEL ?? "kepler";
const user = process.env.KEPLER_USER_ID ?? "";
const sessionId = process.env.KEPLER_SESSION || undefined;
const resume = process.env.KEPLER_RESUME === "1";
const routing = process.env.KEPLER_INPUT_ROUTING === "queue" ? "queue" : "steer";

render(
  <AlternateScreen>
    <AppView cwd={cwd} model={model} user={user} sessionId={sessionId} resume={resume} inputRouting={routing} />
  </AlternateScreen>,
);
