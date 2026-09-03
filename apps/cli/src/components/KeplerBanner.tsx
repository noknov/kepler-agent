import React from "react";
import { Box, Text } from "../cc/kepler-ink.js";
import { keplerMark, theme } from "../lib/theme.js";
import { layoutMetrics } from "../lib/layout.js";
import { useTerminalSize } from "../cc/hooks/useTerminalSize.js";

type Props = {
  cwd: string;
  model: string;
  user: string;
  sessionId: string;
};

export function KeplerBanner({ cwd, model, user, sessionId }: Props) {
  const { rows } = useTerminalSize();
  const { compact } = layoutMetrics(rows);
  const sessionLabel = sessionId.length > 12 ? sessionId.slice(0, 12) : sessionId;

  return (
    <Box flexDirection="column" marginBottom={1}>
      {!compact
        ? keplerMark.map((line) => (
            <Text key={line} color={theme.claude}>
              {"  "}
              {line}
            </Text>
          ))
        : (
          <Text color={theme.claude} bold>
            {"  "}Kepler Agent
          </Text>
        )}
      <Text> </Text>
      <Text dimColor wrap="truncate">
        {"  "}
        {cwd}
      </Text>
      <Text dimColor wrap="truncate">
        {"  "}
        {model} · {user || "session"} · {sessionLabel}
      </Text>
    </Box>
  );
}
