import React, { useMemo, useState } from "react";
import { Box } from "../cc/kepler-ink.js";
import { useTerminalSize } from "../cc/hooks/useTerminalSize.js";
import type { ApprovalRequest } from "../client/appServer.js";
import { filterSlashCommands } from "../lib/slashCommands.js";
import { ApprovalPanel } from "./ApprovalPanel.js";
import { PromptInput } from "./PromptInput.js";
import { SlashMenu } from "./SlashMenu.js";

type Props = {
  busy: boolean;
  connecting: boolean;
  approval: ApprovalRequest | null;
  onSubmitText: (text: string) => void | Promise<void>;
  /** CC REPL: empty→non-empty input re-pins scroll when user isn't reading history. */
  onPromptInput?: (wasEmpty: boolean, next: string) => void;
  onExit: () => void;
};

/**
 * Prompt + slash menu with local input state so keystrokes do not
 * re-render the virtualized transcript (CC REPL pattern).
 */
export function KeplerPromptFooter({
  busy,
  connecting,
  approval,
  onSubmitText,
  onPromptInput,
  onExit,
}: Props) {
  const { columns } = useTerminalSize();
  const [input, setInput] = useState("");
  const [cursorOffset, setCursorOffset] = useState(0);

  const slashMatches = useMemo(() => filterSlashCommands(input), [input]);
  const showSlash = input.startsWith("/") && slashMatches.length > 0 && !busy;

  return (
    <>
      {showSlash ? (
        <Box marginBottom={1}>
          <SlashMenu commands={slashMatches} width={columns} />
        </Box>
      ) : null}
      {approval ? <ApprovalPanel request={approval} /> : null}
      <PromptInput
        value={input}
        onChange={(value) => {
          const wasEmpty = input.trim() === "";
          setInput(value);
          onPromptInput?.(wasEmpty, value);
        }}
        onSubmit={() => {
          const text = input.trim();
          if (!text) {
            return;
          }
          void onSubmitText(text);
          setInput("");
          setCursorOffset(0);
        }}
        onExit={onExit}
        cursorOffset={cursorOffset}
        onChangeCursorOffset={setCursorOffset}
        busy={busy}
        connecting={connecting}
        focus={!approval}
        columns={columns}
      />
    </>
  );
}
