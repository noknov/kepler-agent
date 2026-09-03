import { spawn, type ChildProcess } from "node:child_process";
import type { Readable, Writable } from "node:stream";

export type BackendHandle = {
  process: ChildProcess;
  stdin: Writable;
  stdout: Readable;
  onExit: (listener: (code: number | null, signal: NodeJS.Signals | null) => void) => void;
};

export function spawnBackend(): BackendHandle {
  const binary = process.env.KEPLER_APP_SERVER ?? "app-server";
  const cwd = process.env.KEPLER_CWD ?? process.cwd();
  const exitListeners: Array<(code: number | null, signal: NodeJS.Signals | null) => void> = [];
  const child = spawn(binary, [], {
    cwd,
    env: {
      ...process.env,
      KEPLER_TOKEN: process.env.KEPLER_TOKEN ?? "",
      KEPLER_API_URL: process.env.KEPLER_API_URL ?? "",
    },
    stdio: ["pipe", "pipe", "pipe"],
  });
  if (!child.stdin || !child.stdout) {
    throw new Error("failed to open app-server stdio pipes");
  }
  child.stderr?.on("data", (chunk: Buffer) => {
    if (process.env.KEPLER_DEBUG === "1") {
      process.stderr.write(chunk);
    }
  });
  child.on("error", (error) => {
    for (const listener of exitListeners) {
      listener(null, null);
    }
    console.error(`app-server failed to start (${binary}):`, error.message);
  });
  child.on("exit", (code, signal) => {
    for (const listener of exitListeners) {
      listener(code, signal);
    }
  });
  return {
    process: child,
    stdin: child.stdin,
    stdout: child.stdout,
    onExit: (listener) => {
      exitListeners.push(listener);
    },
  };
}
