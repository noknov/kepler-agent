export function c(slotCount: number): unknown[] {
  return new Array(slotCount).fill(Symbol.for("react.memo_cache_sentinel"));
}
