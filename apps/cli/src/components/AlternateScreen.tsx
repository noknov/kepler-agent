import React, { useEffect, type ReactNode } from "react";

const ENTER_ALT = "\x1b[?1049h\x1b[H\x1b[2J";
const EXIT_ALT = "\x1b[?1049l";

type Props = {
  children: ReactNode;
};

/** Toggles the terminal alternate screen buffer (DEC 1049). */
export function AlternateScreen({ children }: Props) {
  useEffect(() => {
    const stdout = process.stdout;
    if (!stdout.isTTY) {
      return;
    }
    stdout.write(ENTER_ALT);
    return () => {
      stdout.write(EXIT_ALT);
    };
  }, []);

  return <>{children}</>;
}
