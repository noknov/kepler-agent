import { useEffect, useState } from "react";
import { useTerminalSize } from "../cc/hooks/useTerminalSize.js";

export function useTerminalDimensions(): { rows: number; cols: number } {
  const size = useTerminalSize();
  const [dims, setDims] = useState({ rows: size.rows, cols: size.columns });

  useEffect(() => {
    setDims({ rows: size.rows, cols: size.columns });
  }, [size.columns, size.rows]);

  return dims;
}

export function layoutMetrics(rows: number): { compact: boolean; transcriptLines: number } {
  const headerLines = 6;
  const footerLines = 8;
  const promptLines = 3;
  const transcriptLines = Math.max(rows - headerLines - footerLines - promptLines, 8);
  return { compact: rows < 28, transcriptLines };
}
