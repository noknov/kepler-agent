import React from "react";
import type { InputEvent, Key } from "../ink.js";

type PasteHandlerProps = {
  onPaste?: (text: string) => void;
  onInput: (input: string, key: Key) => void;
  onImagePaste?: (
    base64Image: string,
    mediaType?: string,
    filename?: string,
    dimensions?: { width: number; height: number },
    sourcePath?: string,
  ) => void;
};

/**
 * Minimal paste wrapper for Kepler — forwards keystrokes to onInput.
 * The previous stub returned no wrappedOnInput, so BaseTextInput's useInput
 * threw on every keypress (stdin errors were swallowed → frozen UI).
 */
export function usePasteHandler({
  onPaste,
  onInput,
}: PasteHandlerProps): {
  wrappedOnInput: (input: string, key: Key, event: InputEvent) => void;
  isPasting: boolean;
} {
  const [isPasting, setIsPasting] = React.useState(false);
  const chunksRef = React.useRef<string[]>([]);
  const timeoutRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  const flushPaste = React.useCallback(() => {
    timeoutRef.current = null;
    const pastedText = chunksRef.current.join("");
    chunksRef.current = [];
    setIsPasting(false);
    if (!pastedText) {
      return;
    }
    if (onPaste) {
      onPaste(pastedText);
      return;
    }
    onInput(pastedText, { return: false } as Key);
  }, [onInput, onPaste]);

  React.useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
    };
  }, []);

  const wrappedOnInput = React.useCallback(
    (input: string, key: Key, event: InputEvent) => {
      const isFromPaste = event.keypress.isPasted;
      if (isFromPaste) {
        setIsPasting(true);
      }

      const shouldBatch =
        isFromPaste || input.length > 200 || chunksRef.current.length > 0;

      if (shouldBatch) {
        chunksRef.current.push(input);
        if (timeoutRef.current) {
          clearTimeout(timeoutRef.current);
        }
        timeoutRef.current = setTimeout(flushPaste, 100);
        return;
      }

      onInput(input, key);
      if (input.length > 10) {
        setIsPasting(false);
      }
    },
    [flushPaste, onInput],
  );

  return { wrappedOnInput, isPasting };
}
