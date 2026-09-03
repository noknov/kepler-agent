#!/usr/bin/env bash
# Re-sync vendored Claude Code terminal UI from a local checkout.
# Usage: CLAUDE_CODE_SRC=../claude-code/src ./scripts/sync-claude-code-ui.sh
set -euo pipefail
CC="${CLAUDE_CODE_SRC:-/Users/shelton/Documents/claude-code/src}"
DEST="$(cd "$(dirname "$0")/.." && pwd)/src/cc"

mkdir -p "$DEST"/{components/messages,components/Spinner,hooks,context,utils,constants,types,stubs}

# ── Ink core ─────────────────────────────────────────────────────────
cp -R "$CC/ink" "$DEST/"
mkdir -p "$DEST/native-ts"
cp -R "$CC/native-ts/yoga-layout" "$DEST/native-ts/"
cp "$CC/ink.ts" "$DEST/"

# ── Input stack ─────────────────────────────────────────────────────
cp "$CC/components/BaseTextInput.tsx" "$DEST/components/"
cp "$CC/components/TextInput.tsx" "$DEST/components/"
cp "$CC/hooks/useTextInput.ts" "$DEST/hooks/"
cp "$CC/hooks/useDoublePress.ts" "$DEST/hooks/"
cp "$CC/hooks/renderPlaceholder.ts" "$DEST/hooks/"
cp "$CC/hooks/useTerminalSize.ts" "$DEST/hooks/"
cp "$CC/types/textInputTypes.ts" "$DEST/types/"
cp -R "$CC/components/design-system" "$DEST/components/"

# ── Transcript / scroll stack ───────────────────────────────────────
cp "$CC/components/FullscreenLayout.tsx" "$DEST/components/"
cp "$CC/components/VirtualMessageList.tsx" "$DEST/components/"
cp "$CC/components/ScrollKeybindingHandler.tsx" "$DEST/components/"
cp "$CC/hooks/useCopyOnSelect.ts" "$DEST/hooks/"
cp -R "$CC/keybindings" "$DEST/"
# Kepler keeps a stub loadUserBindings (no ~/.claude hot-reload)
cat > "$DEST/keybindings/loadUserBindings.ts" <<'EOF'
/**
 * Kepler stub: default bindings only (no ~/.claude/keybindings.json hot-reload).
 */
import { DEFAULT_BINDINGS } from "./defaultBindings.js";
import { parseBindings } from "./parser.js";
import type { ParsedBinding } from "./types.js";
import { type KeybindingWarning, validateBindings } from "./validate.js";

export type KeybindingsLoadResult = {
  bindings: ParsedBinding[];
  warnings: KeybindingWarning[];
};

export function isKeybindingCustomizationEnabled(): boolean {
  return false;
}

export function loadKeybindingsSyncWithWarnings(): KeybindingsLoadResult {
  const bindings = parseBindings(DEFAULT_BINDINGS);
  const warnings = validateBindings(DEFAULT_BINDINGS);
  return { bindings, warnings };
}

export function initializeKeybindingWatcher(): void {}

export function subscribeToKeybindingChanges(
  _listener: (result: KeybindingsLoadResult) => void,
): () => void {
  return () => undefined;
}
EOF
cp "$CC/utils/array.ts" "$DEST/utils/"
cp "$CC/hooks/useVirtualScroll.ts" "$DEST/hooks/"
cp "$CC/hooks/useBlink.ts" "$DEST/hooks/"
cp "$CC/components/ToolUseLoader.tsx" "$DEST/components/"
cp "$CC/components/MessageResponse.tsx" "$DEST/components/"
cp "$CC/components/OffscreenFreeze.tsx" "$DEST/components/"
cp "$CC/components/messageActions.tsx" "$DEST/components/"
cp "$CC/components/Markdown.tsx" "$DEST/components/"
cp "$CC/components/MarkdownTable.tsx" "$DEST/components/"
cp "$CC/components/messages/UserPromptMessage.tsx" "$DEST/components/messages/"
cp "$CC/components/messages/HighlightedThinkingText.tsx" "$DEST/components/messages/"
cp "$CC/components/messages/AssistantTextMessage.tsx" "$DEST/components/messages/"
cp "$CC/components/Spinner/SpinnerAnimationRow.tsx" "$DEST/components/Spinner/"
cp "$CC/components/Spinner/SpinnerGlyph.tsx" "$DEST/components/Spinner/"
cp "$CC/components/Spinner/index.ts" "$DEST/components/Spinner/"
cp "$CC/components/Spinner/utils.ts" "$DEST/components/Spinner/"
cp "$CC/context/modalContext.tsx" "$DEST/context/"
cp "$CC/context/promptOverlayContext.tsx" "$DEST/context/"
cp "$CC/context/QueuedMessageContext.tsx" "$DEST/context/"
cp "$CC/utils/transcriptSearch.ts" "$DEST/utils/"
cp "$CC/utils/debug.ts" "$DEST/utils/"
cp "$CC/utils/sleep.ts" "$DEST/utils/"
cp "$CC/utils/thinking.ts" "$DEST/utils/"
cp "$CC/utils/formatBriefTimestamp.ts" "$DEST/utils/"
cp "$CC/utils/markdown.ts" "$DEST/utils/"
cp "$CC/utils/hash.ts" "$DEST/utils/"
cp "$CC/constants/figures.ts" "$DEST/constants/"
cp "$CC/constants/messages.ts" "$DEST/constants/"

# ── REPL reference (1:1 copy source — edit screens/KeplerREPLView.tsx from this) ──
mkdir -p "$DEST/screens"
cp "$CC/screens/REPL.tsx" "$DEST/screens/REPL.tsx.bak-full"

for f in Cursor.ts intl.ts sliceAnsi.ts envUtils.ts modifiers.ts fullscreen.ts \
  theme.ts systemTheme.ts execFileNoThrow.ts execFileNoThrowPortable.ts \
  cwd.ts browser.ts stringUtils.ts; do
  cp "$CC/utils/$f" "$DEST/utils/" 2>/dev/null || true
done

echo "Synced Claude Code UI into $DEST"
