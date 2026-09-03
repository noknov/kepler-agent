/** Kepler stub: shell-quote for bash/shellQuote. */
export function quote(args: string[]): string {
  return args.map((a) => `'${a.replace(/'/g, "'\\''")}'`).join(" ");
}

export function parse(input: string): string[] {
  return input.trim().split(/\s+/);
}
