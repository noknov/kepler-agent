import React from "react";
import { Box } from "ink";
import { useRepl } from "./hooks/useRepl.js";
import { FullscreenLayout } from "./components/FullscreenLayout.js";
import { Header } from "./components/Header.js";
import { VirtualTranscript } from "./components/VirtualTranscript.js";
import { PromptInput } from "./components/PromptInput.js";
import { SlashMenu } from "./components/SlashMenu.js";
import { Spinner } from "./components/Spinner.js";
import { ApprovalPanel } from "./components/ApprovalPanel.js";
import { ScrollPill } from "./components/ScrollPill.js";

type Props = {
  cwd: string;
  model: string;
  user: string;
  sessionId?: string;
  resume: boolean;
  inputRouting: "steer" | "queue";
};

export function AppView(props: Props) {
  const repl = useRepl(props);
  const height = (repl.stdout.rows ?? 24) - 8;
  const width = repl.stdout.columns ?? 80;
  const showPill = repl.scrollOffset > 0 || repl.unseen > 0;

  return (
    <FullscreenLayout
      scrollable={
        <Box flexDirection="column">
          <Header
            cwd={props.cwd}
            model={props.model}
            user={props.user}
            sessionId={repl.sessionId ?? "…"}
          />
          <VirtualTranscript
            messages={repl.messages}
            streamText={repl.streamText}
            scrollOffset={repl.scrollOffset}
            height={height}
          />
          {repl.busy ? <Spinner frame={repl.spinnerFrame} verb={repl.spinnerVerb} /> : null}
          {repl.approval ? <ApprovalPanel request={repl.approval} /> : null}
        </Box>
      }
      bottom={<PromptInput value={repl.input} busy={repl.busy} />}
      modal={repl.showSlash ? <SlashMenu commands={repl.slashMatches} width={width} /> : undefined}
      pill={showPill ? <ScrollPill unseen={repl.unseen} /> : undefined}
    />
  );
}
