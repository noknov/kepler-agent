export const API_ERROR_MESSAGE_PREFIX = "";
export const API_TIMEOUT_ERROR_MESSAGE = "";
export const CREDIT_BALANCE_TOO_LOW_ERROR_MESSAGE = "";
export const CUSTOM_OFF_SWITCH_MESSAGE = "";
export const INVALID_API_KEY_ERROR_MESSAGE = "";
export const INVALID_API_KEY_ERROR_MESSAGE_EXTERNAL = "";
export const ORG_DISABLED_ERROR_MESSAGE_ENV_KEY = "";
export const ORG_DISABLED_ERROR_MESSAGE_ENV_KEY_WITH_OAUTH = "";
export const PROMPT_TOO_LONG_ERROR_MESSAGE = "";
export const TOKEN_REVOKED_ERROR_MESSAGE = "";

export function startsWithApiErrorPrefix(_text: string): boolean {
  return false;
}

export function getImageTooLargeErrorMessage(): string { return "Image too large"; }
export function getPdfInvalidErrorMessage(): string { return "Invalid PDF"; }
export function getPdfPasswordProtectedErrorMessage(): string { return "PDF password protected"; }
export function getPdfTooLargeErrorMessage(): string { return "PDF too large"; }
export function getRequestTooLargeErrorMessage(): string { return "Request too large"; }
