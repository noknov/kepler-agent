export function getKairosActive(): boolean {
  return false;
}

export function getUserMsgOptIn(): boolean {
  return false;
}

export function getIsInteractive(): boolean {
  return true;
}

export function getCwdState(): string {
  return process.cwd();
}

export function getOriginalCwd(): string {
  return process.cwd();
}

export function updateLastInteractionTime(_immediate?: boolean): void {
  // no-op
}

export function flushInteractionTime(): void {
  // no-op
}

export function markScrollActivity(): void {
  // no-op
}

export function getIsRemoteMode(): boolean {
  return false;
}

export function waitForScrollIdle(): Promise<void> {
  return Promise.resolve();
}

export function getIsNonInteractiveSession(): boolean {
  return false;
}

export function getSessionTrustAccepted(): boolean {
  return true;
}

export function getCommitCounter(): number { return 0; }
export function getPrCounter(): number { return 0; }
export function isReplBridgeActive(): boolean { return false; }

export function getStrictToolResultPairing(): boolean { return true; }

export function getSessionId(): string { return ""; }
