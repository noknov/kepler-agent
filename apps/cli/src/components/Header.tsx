import React from "react";
import { Box, Text } from "ink";
import { keplerMark, theme } from "../lib/theme.js";

type Props = {
  cwd: string;
  model: string;
  user: string;
  sessionId: string;
};

export function Header({ cwd, model, user, sessionId }: Props) {
  return (
    <Box flexDirection="column" marginBottom={1}>
      {keplerMark.map((line) => (
        <Text key={line} color={theme.claude}>
          {"  "}
          {line}
        </Text>
      ))}
      <Text> </Text>
      <Text dimColor>
        {"  "}
        {cwd}
      </Text>
      <Text dimColor>
        {"  "}
        {model} · {user || "session"} · {sessionId.slice(0, 12)}
      </Text>
    </Box>
  );
}
