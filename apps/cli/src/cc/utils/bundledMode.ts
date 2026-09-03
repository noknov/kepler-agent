export function isRunningWithBun(): boolean {
  return process.versions.bun !== undefined;
}

export function isInBundledMode(): boolean {
  return false;
}
