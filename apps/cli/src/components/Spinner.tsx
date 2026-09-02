import React from "react";
import { Text } from "ink";
import { spinnerGlyph } from "../lib/spinner.js";
import { theme } from "../lib/theme.js";

type Props = {
  frame: number;
  verb: string;
};

export function Spinner({ frame, verb }: Props) {
  return (
    <Text>
      {"  "}
      <Text color={theme.claude}>{spinnerGlyph(frame)}</Text> {verb}…
    </Text>
  );
}
