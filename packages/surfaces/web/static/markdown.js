export function renderMarkdown(text) {
  if (!text) return "";
  if (typeof marked === "undefined") {
    return escapeHTML(text).replace(/\n/g, "<br>");
  }

  const raw = marked.parse(String(text), { breaks: true, gfm: true });
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
