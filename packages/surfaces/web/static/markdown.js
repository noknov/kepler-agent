function normalizeMarkdownSource(text) {
  let source = String(text).replace(/\r\n/g, "\n");
  // Common LLM mistake: two-backtick fences instead of triple-backtick fences.
  source = source.replace(/^\s*``[ \t]*([a-zA-Z0-9_-]*)?[ \t]*$/gm, (_, lang) => `\`\`\`${lang || ""}`);
  return source;
}

export function renderMarkdown(text) {
  if (!text) return "";
  if (typeof marked === "undefined") {
    return escapeHTML(text).replace(/\n/g, "<br>");
  }

  const normalized = normalizeMarkdownSource(text);
  const raw = marked.parse(normalized, { breaks: true, gfm: true });
  if (typeof DOMPurify === "undefined") {
    // Never turn a transient asset failure into an HTML injection path.
    return escapeHTML(text).replace(/\n/g, "<br>");
  }
  return DOMPurify.sanitize(raw, { USE_PROFILES: { html: true } });
}

function escapeHTML(text) {
  return String(text).replace(/[&<>"']/g, (char) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#039;",
  }[char]));
}
