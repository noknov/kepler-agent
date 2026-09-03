/**
 * Kepler stub: classifier approval tracking for bash auto-approve (CC-specific).
 */
export function getClassifierApproval(_toolUseID: string): string | undefined {
  return undefined;
}

export function getYoloClassifierApproval(_toolUseID: string): string | undefined {
  return undefined;
}

export function deleteClassifierApproval(_toolUseID: string): void {}

const checking = new Set<string>();
const listeners = new Set<() => void>();

export function isClassifierChecking(toolUseID: string): boolean {
  return checking.has(toolUseID);
}

export function subscribeClassifierChecking(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
