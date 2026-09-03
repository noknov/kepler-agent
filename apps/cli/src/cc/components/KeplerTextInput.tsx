import React, { useMemo } from "react";
import chalk from "chalk";
import { useTextInput } from "../hooks/useTextInput.js";
import { Box, useTerminalFocus } from "../kepler-ink.js";
import type { BaseTextInputProps } from "../types/textInputTypes.js";
import { BaseTextInput } from "./BaseTextInput.js";

type Props = BaseTextInputProps & {
  prefix?: string;
};

/** Claude Code TextInput without voice/clipboard/settings — Kepler-trimmed. */
export function KeplerTextInput(props: Props): React.ReactNode {
  const terminalFocus = useTerminalFocus();
  const invert = useMemo(() => chalk.inverse, []);

  const inputState = useTextInput({
    value: props.value,
    onChange: props.onChange,
    onSubmit: props.onSubmit,
    onExit: props.onExit,
    onExitMessage: props.onExitMessage,
    onHistoryReset: props.onHistoryReset,
    onClearInput: props.onClearInput,
    focus: props.focus,
    mask: props.mask,
    multiline: props.multiline,
    cursorChar: props.showCursor ? " " : "",
    invert,
    themeText: (text: string) => text,
    columns: props.columns,
    maxVisibleLines: props.maxVisibleLines,
    disableCursorMovementForUpDownKeys: props.disableCursorMovementForUpDownKeys,
    disableEscapeDoublePress: props.disableEscapeDoublePress,
    externalOffset: props.cursorOffset,
    onOffsetChange: props.onChangeCursorOffset,
    inputFilter: props.inputFilter,
    inlineGhostText: props.inlineGhostText,
    dim: chalk.dim,
  });

  return (
    <Box flexDirection="row" width="100%">
      {props.prefix ? <Box marginRight={1}>{props.prefix}</Box> : null}
      <BaseTextInput
        inputState={inputState}
        terminalFocus={terminalFocus}
        invert={invert}
        {...props}
      />
    </Box>
  );
}
