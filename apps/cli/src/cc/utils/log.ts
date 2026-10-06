import { writeSync } from 'node:fs'

export function logError(error: Error): void {
  // Ink redirects console.error back here and intercepts stderr.write too.
  // Write to the descriptor directly so diagnostics cannot re-enter either.
  try {
    writeSync(2, `[kepler-cli] ${error.stack ?? error.message}\n`)
  } catch {
    // A closed diagnostic descriptor must not interrupt input handling.
  }
}

export function logMCPDebug(_message: string): void {
  // no-op
}
