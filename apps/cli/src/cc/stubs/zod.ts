/** Kepler stub: minimal zod stand-in for CC type-only imports. */
export type infer<T> = T extends { _output: infer O } ? O : unknown;

export const z = {
  object: <T extends Record<string, unknown>>(shape: T) => ({ parse: (v: unknown) => v, safeParse: (v: unknown) => ({ success: true, data: v }), shape }),
  string: () => ({}),
  number: () => ({}),
  boolean: () => ({}),
  unknown: () => ({}),
  array: (inner: unknown) => ({ parse: (v: unknown) => v }),
  enum: <T extends readonly [string, ...string[]]>(values: T) => ({ parse: (v: unknown) => v }),
  optional: (inner: unknown) => inner,
  nullable: (inner: unknown) => inner,
};

export type ZodType<T = unknown> = {
  parse: (v: unknown) => T;
  safeParse: (v: unknown) => { success: boolean; data?: T };
};
