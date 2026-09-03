import React from "react";
import { Text } from "../../ink.js";

export function HighlightedInput(props: { value: string }): React.ReactNode {
  return <Text>{props.value}</Text>;
}
