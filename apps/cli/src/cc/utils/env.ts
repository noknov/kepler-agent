import { isEnvTruthy } from "./envUtils.js";

function isSSHSession(): boolean {
  return !!(process.env.SSH_CONNECTION || process.env.SSH_CLIENT || process.env.SSH_TTY);
}

function detectTerminal(): string | null {
  if (process.env.CURSOR_TRACE_ID) return "cursor";
  if (process.env.VSCODE_GIT_ASKPASS_MAIN?.includes("cursor")) return "cursor";
  if (process.env.TERM_PROGRAM) return process.env.TERM_PROGRAM;
  if (process.env.TMUX) return "tmux";
  if (process.env.WT_SESSION) return "windows-terminal";
  if (isSSHSession()) return "ssh-session";
  return null;
}

export const env = {
  isCI: isEnvTruthy(process.env.CI),
  platform: (["win32", "darwin"].includes(process.platform) ? process.platform : "linux") as
    | "win32"
    | "darwin"
    | "linux",
  arch: process.arch,
  nodeVersion: process.version,
  terminal: detectTerminal(),
};

export function getGlobalClaudeFile(): string {
  return `${process.env.HOME ?? ""}/.claude.json`;
}
