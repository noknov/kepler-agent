import React, { useEffect, useState } from "react";
import { Box, Text } from "../cc/kepler-ink.js";
import { SpinnerGlyph } from "../cc/components/Spinner/SpinnerGlyph.js";
import { randomSpinnerVerb } from "../lib/spinner.js";
import { theme } from "../lib/theme.js";

export function KeplerSpinner({ busy }: { busy: boolean }) {
  const [frame, setFrame] = useState(0);
  const [verb] = useState(() => randomSpinnerVerb());

  useEffect(() => {
    if (!busy) {
      return;
    }
    const timer = setInterval(() => setFrame((value) => value + 1), 120);
    return () => clearInterval(timer);
  }, [busy]);

  if (!busy) {
    return null;
  }

  return (
    <Box flexDirection="row" marginTop={1} width="100%">
      <SpinnerGlyph frame={frame} messageColor="claude" />
      <Text color={theme.claude}> {verb}…</Text>
    </Box>
  );
}
