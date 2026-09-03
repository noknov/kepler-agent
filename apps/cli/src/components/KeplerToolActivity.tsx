import React, { useEffect, useState } from "react";
import { Box, Text } from "../cc/kepler-ink.js";
import { toolDisplayName } from "../lib/toolDisplay.js";
import { theme } from "../lib/theme.js";
import type { ActiveTool } from "../hooks/useRepl.js";

export function KeplerToolActivity({ tools }: { tools: ActiveTool[] }) {
  const [visible, setVisible] = useState(true);

  useEffect(() => {
    if (tools.length === 0) {
      return;
    }
    const timer = setInterval(() => setVisible((value) => !value), 420);
    return () => clearInterval(timer);
  }, [tools.length]);

  if (tools.length === 0) {
    return null;
  }

  return (
    <Box flexDirection="column" width="100%">
      {tools.map((tool) => (
        <Box key={tool.id} flexDirection="row" minWidth={2}>
          <Text color={theme.claude}>{visible ? "⏺" : " "}</Text>
          <Text> {toolDisplayName(tool.name)}{tool.detail ? ` · ${tool.detail}` : ""}</Text>
        </Box>
      ))}
    </Box>
  );
}
