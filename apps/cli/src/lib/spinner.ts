const glyphs = ["·", "✢", "✳", "✶", "✻", "✽"];

const verbs = [
  "Accomplishing",
  "Brewing",
  "Calculating",
  "Composing",
  "Conjuring",
  "Cooking",
  "Crafting",
  "Deliberating",
  "Dreaming",
  "Forging",
  "Pondering",
  "Thinking",
  "Weaving",
];

export function randomSpinnerVerb(): string {
  return verbs[Math.floor(Math.random() * verbs.length)] ?? "Thinking";
}

export function spinnerGlyph(frame: number): string {
  const pingPong = glyphs.length * 2 - 2;
  const index = frame % pingPong;
  return index < glyphs.length ? glyphs[index]! : glyphs[pingPong - index]!;
}
