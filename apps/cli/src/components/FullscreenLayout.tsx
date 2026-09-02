import React, { type ReactNode } from "react";
import { Box, useStdout } from "ink";

type Props = {
  scrollable: ReactNode;
  bottom: ReactNode;
  modal?: ReactNode;
  pill?: ReactNode;
};

export function FullscreenLayout({ scrollable, bottom, modal, pill }: Props) {
  const { stdout } = useStdout();
  const height = stdout.rows ?? 24;
  const bottomLines = 3;
  const scrollHeight = Math.max(height - bottomLines, 1);

  return (
    <Box flexDirection="column" height={height}>
      <Box flexDirection="column" height={scrollHeight} overflow="hidden">
        {scrollable}
        {pill ? (
          <Box marginTop={-1} justifyContent="center">
            {pill}
          </Box>
        ) : null}
      </Box>
      <Box flexShrink={0} flexDirection="column">
        {bottom}
      </Box>
      {modal ? (
        <Box flexDirection="column" marginTop={-bottomLines - 6}>
          {modal}
        </Box>
      ) : null}
    </Box>
  );
}
