/** Mirrors kepler-agent/cli/renderer.go — human-readable tool chrome, not raw JSON. */

const SUMMARY_MAX = 56;

const QUIET_TOOLS = new Set(["agent-explore", "tool_search"]);

export function shouldShowToolSummary(name: string): boolean {
  return !QUIET_TOOLS.has(name);
}

export function toolDisplayName(name: string): string {
  switch (name) {
    case "agent-explore":
      return "Explore";
    case "read_file":
      return "Read";
    case "write_file":
      return "Write";
    case "edit_file":
      return "Edit";
    case "list_files":
      return "List";
    case "skill_load":
      return "Skill";
    case "bash":
    case "exec":
      return "Bash";
    case "grep":
      return "Grep";
    case "glob":
      return "Glob";
    default:
      return name;
  }
}

export function summarizeToolArgs(raw: unknown): string {
  let obj: Record<string, unknown> | null = null;
  if (typeof raw === "string") {
    try {
      obj = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return clipWidth(raw.replace(/\s+/g, " ").trim(), SUMMARY_MAX);
    }
  } else if (raw && typeof raw === "object" && !Array.isArray(raw)) {
    obj = raw as Record<string, unknown>;
  }
  if (!obj) {
    return "";
  }

  const tasks = obj.tasks;
  if (Array.isArray(tasks) && tasks.length > 0) {
    const first = tasks[0];
    if (first && typeof first === "object" && !Array.isArray(first)) {
      const task = String((first as Record<string, unknown>).task ?? "").trim();
      if (task) {
        if (tasks.length > 1) {
          return `${clipWidth(task, 44)} · ${tasks.length} tasks`;
        }
        return clipWidth(task, SUMMARY_MAX);
      }
    }
  }

  // Prefer task over boundaries — boundaries is internal scope, not user-facing.
  for (const key of ["task", "command", "path", "file_path", "query", "glob", "pattern", "url", "name", "description"]) {
    const value = obj[key];
    if (typeof value === "string" && value.trim()) {
      return clipWidth(value.trim(), SUMMARY_MAX);
    }
  }

  const paths = obj.paths;
  if (Array.isArray(paths) && paths.length > 0) {
    const path = String(paths[0] ?? "");
    if (path) {
      if (paths.length > 1) {
        return `${clipWidth(path, 40)} +${paths.length - 1}`;
      }
      return clipWidth(path, SUMMARY_MAX);
    }
  }

  return "";
}

function clipWidth(value: string, max: number): string {
  if (value.length <= max) {
    return value;
  }
  return `${value.slice(0, max - 1)}…`;
}
