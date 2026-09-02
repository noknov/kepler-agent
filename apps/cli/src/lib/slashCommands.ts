export type SlashCommand = {
  name: string;
  description: string;
};

export const slashCommands: SlashCommand[] = [
  { name: "/help", description: "Show available commands" },
  { name: "/status", description: "Session, model, and workspace" },
  { name: "/clear", description: "Clear the transcript" },
  { name: "/exit", description: "Exit the UI" },
];

export function filterSlashCommands(input: string): SlashCommand[] {
  if (!input.startsWith("/")) {
    return [];
  }
  const query = input.toLowerCase();
  return slashCommands.filter((command) => command.name.startsWith(query));
}
