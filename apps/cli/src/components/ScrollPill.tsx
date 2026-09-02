import React from "react";
import { Text } from "ink";
import { theme } from "../lib/theme.js";

type Props = {
  unseen: number;
};

export function ScrollPill({ unseen }: Props) {
  const label = unseen > 0 ? `${unseen} new message${unseen === 1 ? "" : "s"}` : "Jump to bottom";
  return (
    <Text color={theme.claude} inverse>
      {" "}
      {label} (g)
      {" "}
    </Text>
  );
}
