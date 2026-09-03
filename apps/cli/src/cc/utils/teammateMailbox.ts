/** Kepler stub: teammate mailbox for swarm attachments (CC-specific). */
export type PlanApprovalRequestMessage = { from: string; planContent: string; planFilePath?: string };
export type PlanApprovalResponseMessage = { from: string; approved: boolean };
export type IdleNotificationMessage = { from: string };

export function isPlanApprovalRequest(_att: unknown): _att is PlanApprovalRequestMessage {
  return false;
}
export function isPlanApprovalResponse(_att: unknown): _att is PlanApprovalResponseMessage {
  return false;
}
export function isIdleNotification(_att: unknown): _att is IdleNotificationMessage {
  return false;
}
export function isShutdownApproved(_att: unknown): boolean {
  return false;
}
