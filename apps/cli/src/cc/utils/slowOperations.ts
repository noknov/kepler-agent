export function slowLogging<T>(_label: string, fn: () => T): T {
  return fn();
}

export function jsonStringify(value: unknown): string {
  return JSON.stringify(value);
}

export function jsonParse<T = unknown>(text: string): T {
  return JSON.parse(text) as T;
}
