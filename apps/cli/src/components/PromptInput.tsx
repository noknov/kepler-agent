import React from "react";
import { Box, Text, useStdout } from "ink";
import { theme } from "../lib/theme.js";

type Props = {
  value: string;
  busy: boolean;
  placeholder?: string;
};

export function PromptInput({ value, busy, placeholder = "Type a message…" }: Props) {
  const { stdout } = useStdout();
  const width = stdout.columns ?? 80;
  const border = "─".repeat(Math.max(width - 2, 20));
  const line = busy && !value ? "…" : value || placeholder;
  const dim = !value || busy;

  return (
    <Box flexDirection="column">
      <Text color={theme.border}>{"╭" + border + "╮"}</Text>
      <Box>
        <Text color={theme.claude}>❯ </Text>
        <Text dimColor={dim}>{line}</Text>
      </Box>
      <Text color={theme.border}>{"╰" + border + "╯"}</Text>
    </Box>
  );
}
