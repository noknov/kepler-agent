/**
 * Kepler stub: primitive tools registry pulls entire tool tree.
 * Kepler passes tools via props; REPL primitive lookup returns empty.
 */
import type { Tools } from "../../Tool.js";

export function getReplPrimitiveTools(): Tools {
  return [];
}
