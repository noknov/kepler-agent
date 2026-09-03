import React from "react";
import { Box, Text } from "../cc/kepler-ink.js";
import stringWidth from "string-width";
import { KeplerTextInput } from "../cc/components/KeplerTextInput.js";
import { theme } from "../lib/theme.js";

const PROMPT_PREFIX = "❯ ";
const PROMPT_LINES = 3;

type Props = {
  value: string;
  onChange: (value: string) => void;
  onSubmit: () => void;
  onExit?: () => void;
  cursorOffset: number;
  onChangeCursorOffset: (offset: number) => void;
  busy: boolean;
  connecting?: boolean;
  focus?: boolean;
  columns: number;
  placeholder?: string;
};

export function PromptInput({
  value,
  onChange,
  onSubmit,
  onExit,
  cursorOffset,
  onChangeCursorOffset,
  busy,
  connecting = false,
  focus = true,
  columns,
  placeholder = "Type a message…",
}: Props) {
  const innerWidth = Math.max(columns - 2, 20);
  const border = "─".repeat(innerWidth);
  const disabled = connecting || (busy && !value);

  return (
    <Box flexDirection="column" width="100%" height={PROMPT_LINES} flexShrink={0}>
      <Text color={theme.border}>{"╭" + border + "╮"}</Text>
      <Box height={1} flexDirection="row">
        <Text color={theme.border}>{PROMPT_PREFIX}</Text>
        <Box flexGrow={1}>
          {connecting ? (
            <Text dimColor>Connecting to app-server…</Text>
          ) : (
            <KeplerTextInput
              value={value}
              onChange={onChange}
              onSubmit={onSubmit}
              onExit={onExit}
              focus={focus && !disabled}
              showCursor
              columns={Math.max(columns - stringWidth(PROMPT_PREFIX) - 2, 16)}
              cursorOffset={cursorOffset}
              onChangeCursorOffset={onChangeCursorOffset}
              placeholder={placeholder}
              disableCursorMovementForUpDownKeys
            />
          )}
        </Box>
      </Box>
      <Text color={theme.border}>{"╰" + border + "╯"}</Text>
    </Box>
  );
}
