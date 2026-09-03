export type Platform = "macos" | "windows" | "wsl" | "linux" | "unknown";

export function getPlatform(): Platform {
  if (process.platform === "darwin") {
    return "macos";
  }
  if (process.platform === "win32") {
    return "windows";
  }
  if (process.platform === "linux") {
    return "linux";
  }
  return "unknown";
}
