import React from "react";
import { Box, Text } from "../cc/kepler-ink.js";
import type { ApprovalRequest } from "../client/appServer.js";
import { theme } from "../lib/theme.js";

type Props = {
  request: ApprovalRequest;
};

export function ApprovalPanel({ request }: Props) {
  return (
    <Box flexDirection="column" marginTop={1}>
      <Text color="yellow">! {request.toolName}</Text>
		<Text dimColor> {request.summary}</Text>
		{request.details && request.details !== request.summary ? <Text dimColor>{request.details}</Text> : null}
      <Text dimColor> [o]nce [s]ession [p]roject [n]o</Text>
    </Box>
  );
}
