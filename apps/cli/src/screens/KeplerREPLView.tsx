/**
 * Kepler transcript shell — copied verbatim from claude-code/src/screens/REPL.tsx
 * (mainReturn scrollable block + deferred/streaming/spinner logic). Only Kepler
 * wiring (KeplerBanner, KeplerPromptFooter) is substituted.
 */
import React, {
  useCallback,
  useDeferredValue,
  useMemo,
  type RefObject,
} from "react";
import { Box } from "../cc/kepler-ink.js";
import type { ScrollBoxHandle } from "../cc/kepler-ink.js";
import { FullscreenLayout } from "../cc/components/FullscreenLayout.js";
import type { UnseenDivider } from "../cc/components/FullscreenLayout.js";
import { Messages } from "../cc/components/Messages.js";
import { commands } from "../cc/commands.js";
import type { Tools } from "../cc/Tool.js";
import type { RenderableMessage } from "../cc/types/message.js";
import { isFullscreenEnvEnabled } from "../cc/utils/fullscreen.js";
import { hasCursorUpViewportYankBug } from "../cc/ink/terminal.js";
import { KeplerBanner } from "../components/KeplerBanner.js";
import { KeplerSpinner } from "../components/KeplerSpinner.js";
import { KeplerToolActivity } from "../components/KeplerToolActivity.js";
import type { ActiveTool } from "../hooks/useRepl.js";

const EMPTY_TOOLS: Tools = [];
const TOOL_JSX = null;

type ReplChrome = {
  scrollRef: RefObject<ScrollBoxHandle | null>;
  dividerYRef: RefObject<number | null>;
  jumpToNew: (handle: ScrollBoxHandle | null) => void;
  unseenDivider: UnseenDivider | undefined;
  messages: RenderableMessage[];
  streamingText: string | null;
  busy: boolean;
  inProgressToolUseIDs: Set<string>;
  activeTools: ActiveTool[];
  sessionId: string | null;
};

type Props = ReplChrome & {
  cwd: string;
  model: string;
  user: string;
  bottom: React.ReactNode;
};

export function KeplerREPLView({
  scrollRef,
  dividerYRef,
  jumpToNew,
  unseenDivider,
  messages,
  streamingText,
  busy: isLoading,
  inProgressToolUseIDs,
  activeTools,
  sessionId,
  cwd,
  model,
  user,
  bottom,
}: Props) {
  // REPL.tsx L1315-1322 — deferred messages for input responsiveness during stream.
  const deferredMessages = useDeferredValue(messages);

  // Render every provider delta immediately. The vendored REPL only renders
  // completed lines as a compatibility workaround for a few terminals; that
  // makes Kepler appear to stream one line at a time.
  const showStreamingText = !hasCursorUpViewportYankBug();
  const visibleStreamingText =
    streamingText && showStreamingText
      ? streamingText
      : null;

  // REPL.tsx L4506-4509 — sync messages when streaming text shows or turn ended.
  const usesSyncMessages = showStreamingText || !isLoading;
  const displayedMessages = usesSyncMessages ? messages : deferredMessages;

  // REPL.tsx L1672-1685 — Kepler stubs: no toolJSX, queues, teammates, brief mode.
  const showSpinner = isLoading && !visibleStreamingText;

  const onPillClick = useCallback(() => {
    jumpToNew(scrollRef.current);
  }, [jumpToNew, scrollRef]);

  const conversationId = useMemo(
    () => sessionId ?? "pending",
    [sessionId],
  );

  return (
  // REPL.tsx L4565-4590 — FullscreenLayout scrollable + bottom.
    <FullscreenLayout
      scrollRef={scrollRef}
      dividerYRef={dividerYRef}
      hidePill
      hideSticky
      newMessageCount={unseenDivider?.count ?? 0}
      onPillClick={onPillClick}
      scrollable={
        <>
          <KeplerBanner
            cwd={cwd}
            model={model}
            user={user}
            sessionId={sessionId ?? "…"}
          />
          <Messages
            messages={displayedMessages}
            tools={EMPTY_TOOLS}
            commands={commands}
            verbose={false}
            toolJSX={TOOL_JSX}
            toolUseConfirmQueue={[]}
            inProgressToolUseIDs={inProgressToolUseIDs}
            isMessageSelectorVisible={false}
            conversationId={conversationId}
            screen="prompt"
            streamingToolUses={[]}
            hideLogo
            isLoading={isLoading}
            streamingText={isLoading ? visibleStreamingText : null}
            unseenDivider={unseenDivider}
            scrollRef={isFullscreenEnvEnabled() ? scrollRef : undefined}
            trackStickyPrompt={isFullscreenEnvEnabled() ? true : undefined}
          />
          <KeplerToolActivity tools={activeTools} />
          <Box flexGrow={1} />
          {showSpinner ? <KeplerSpinner busy={isLoading} /> : null}
        </>
      }
      bottom={bottom}
    />
  );
}
