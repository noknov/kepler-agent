import React from "react";
import { Box, useApp } from "./cc/kepler-ink.js";
import { AlternateScreen } from "./cc/kepler-ink.js";
import { AppStateProvider } from "./cc/state/AppState.js";
import { ScrollKeybindingHandler } from "./cc/components/ScrollKeybindingHandler.js";
import { KeybindingSetup } from "./cc/keybindings/KeybindingProviderSetup.js";
import { isFullscreenEnvEnabled } from "./cc/utils/fullscreen.js";
import { useRepl } from "./hooks/useRepl.js";
import { KeplerPromptFooter } from "./components/KeplerPromptFooter.js";
import { KeplerREPLView } from "./screens/KeplerREPLView.js";

type Props = {
  cwd: string;
  model: string;
  user: string;
  sessionId?: string;
  resume: boolean;
  inputRouting: "steer" | "queue";
};

export function AppView(props: Props) {
  const { exit } = useApp();
  const repl = useRepl(props);
  const connecting = repl.connectionState === "connecting";
  return (
    <>
      <ScrollKeybindingHandler
        scrollRef={repl.scrollRef}
        isActive={isFullscreenEnvEnabled()}
        onScroll={repl.composedOnScroll}
      />
      <Box flexDirection="column" flexGrow={1} minHeight={0} width="100%">
        <KeplerREPLView
          scrollRef={repl.scrollRef}
          dividerYRef={repl.dividerYRef}
          jumpToNew={repl.jumpToNew}
          unseenDivider={repl.unseenDivider}
          messages={repl.messages}
          streamingText={repl.streamingText}
          busy={repl.busy}
          inProgressToolUseIDs={repl.inProgressToolUseIDs}
          sessionId={repl.sessionId}
          cwd={props.cwd}
          model={props.model}
          user={props.user}
          bottom={
            <KeplerPromptFooter
              busy={repl.busy}
              connecting={connecting}
              approval={repl.approval}
              onSubmitText={repl.submitText}
              onPromptInput={repl.repinOnPromptInput}
              onExit={exit}
            />
          }
        />
      </Box>
    </>
  );
}

export function App(props: Props) {
  return (
    <AppStateProvider>
      <KeybindingSetup>
        <AlternateScreen mouseTracking>
          <AppView {...props} />
        </AlternateScreen>
      </KeybindingSetup>
    </AppStateProvider>
  );
}
