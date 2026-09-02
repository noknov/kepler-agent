import React, { useMemo } from "react";
import { Box, Text } from "ink";
import { flattenMessages, type Message } from "../lib/messages.js";
import { theme } from "../lib/theme.js";

type Props = {
  messages: Message[];
  streamText: string;
  scrollOffset: number;
  height: number;
};

export function VirtualTranscript({ messages, streamText, scrollOffset, height }: Props) {
  const lines = useMemo(() => {
    const base = flattenMessages(messages);
    if (streamText.trim()) {
      for (const chunk of streamText.split("\n")) {
        base.push(`◆ ${chunk}`);
      }
    }
    return base;
  }, [messages, streamText]);

  const visibleHeight = Math.max(height - 1, 1);
  const maxOffset = Math.max(lines.length - visibleHeight, 0);
  const offset = Math.min(scrollOffset, maxOffset);
  const start = Math.max(lines.length - visibleHeight - offset, 0);
  const visible = lines.slice(start, start + visibleHeight);
  const pad = visibleHeight - visible.length;

  return (
    <Box flexDirection="column" height={visibleHeight} overflow="hidden">
      {Array.from({ length: pad }).map((_, index) => (
        <Text key={`pad-${index}`}> </Text>
      ))}
      {visible.map((line, index) => (
        <Text key={`${start + index}`} wrap="truncate">
          {colorize(line)}
        </Text>
      ))}
    </Box>
  );
}

function colorize(line: string): React.ReactNode {
  if (line.startsWith("❯ ")) {
    return <Text color={theme.claude}>{line}</Text>;
  }
  if (line.startsWith("◆ ")) {
    return <Text>{line}</Text>;
  }
  if (line.startsWith("⚙ ")) {
    return <Text color={theme.tool}>{line}</Text>;
  }
  if (line.startsWith("↪ ")) {
    return <Text dimColor>{line}</Text>;
  }
  return <Text dimColor>{line}</Text>;
}

export function maxScrollOffset(messageCount: number, streamText: string, visibleHeight: number): number {
  const lines = messageCount + (streamText ? streamText.split("\n").length : 0);
  return Math.max(lines - visibleHeight, 0);
}
