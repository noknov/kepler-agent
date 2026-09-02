import { spawn, type ChildProcess } from "node:child_process";
import type { Readable, Writable } from "node:stream";

export type BackendHandle = {
  process: ChildProcess;
  stdin: Writable;
  stdout: Readable;
};

export function spawnBackend(): BackendHandle {
  const binary = process.env.KEPLER_APP_SERVER ?? "app-server";
  const cwd = process.env.KEPLER_CWD ?? process.cwd();
  const child = spawn(binary, [], {
    cwd,
    env: {
      ...process.env,
      KEPLER_TOKEN: process.env.KEPLER_TOKEN ?? "",
      KEPLER_API_URL: process.env.KEPLER_API_URL ?? "",
    },
    stdio: ["pipe", "pipe", "inherit"],
  });
  if (!child.stdin || !child.stdout) {
    throw new Error("failed to open app-server stdio pipes");
  }
  child.on("error", (error) => {
    console.error(`app-server failed to start (${binary}):`, error.message);
  });
  return { process: child, stdin: child.stdin, stdout: child.stdout };
}
