import React from "react";
import { Box, Text } from "ink";
import type { SlashCommand } from "../lib/slashCommands.js";
import { theme } from "../lib/theme.js";

type Props = {
  commands: SlashCommand[];
  width: number;
};

export function SlashMenu({ commands, width }: Props) {
  const border = "─".repeat(Math.max(width - 2, 20));
  return (
    <Box flexDirection="column">
      <Text color={theme.border}>{"▔".repeat(Math.min(width, 72))}</Text>
      <Box flexDirection="column" paddingX={1}>
        {commands.length === 0 ? (
          <Text dimColor>No matching commands</Text>
        ) : (
          commands.map((command) => (
            <Text key={command.name}>
              <Text color={theme.claude}>{command.name}</Text>
              <Text dimColor> — {command.description}</Text>
            </Text>
          ))
        )}
      </Box>
      <Text color={theme.border}>{border}</Text>
    </Box>
  );
}
