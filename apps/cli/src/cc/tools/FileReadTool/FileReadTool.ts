/** Kepler stub: tool registry imports for messages.ts (full tools not needed for rendering). */
export const FileReadTool = { name: "Read", outputSchema: { safeParse: (v: unknown) => ({ success: true, data: v }) } };
export type Output = { content?: string; file?: { filePath?: string } };
