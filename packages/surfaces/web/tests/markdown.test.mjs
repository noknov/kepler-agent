import assert from 'node:assert/strict';
import test from 'node:test';
import {readFileSync} from 'node:fs';
import vm from 'node:vm';

function renderer({parser = true, sanitizer = true} = {}) {
  const context = vm.createContext({});
  if (parser) vm.runInContext(readFileSync(new URL('../static/vendor/marked.min.js', import.meta.url), 'utf8'), context);
  // The real parser exercises Markdown semantics. The sanitizer boundary is
  // stubbed here; its security behavior belongs to the vendored DOMPurify.
  if (sanitizer) context.DOMPurify = {sanitize: html => html};
  const source = readFileSync(new URL('../static/markdown.js', import.meta.url), 'utf8');
  vm.runInContext(source.replace('export function', 'function'), context);
  return context.renderMarkdown;
}

test('backticks inside fenced literal text are preserved', () => {
  const html = renderer()('```text\n``\n**literal**\n```');
  assert.match(html, /<code[^>]*>``\n\*\*literal\*\*/);
  assert.doesNotMatch(html, /<strong>/);
});

test('standard Markdown controls emphasis and multiline code spans', () => {
  const render = renderer();
  assert.match(render('**bold** and *italic*'), /<strong>bold<\/strong> and <em>italic<\/em>/);
  assert.match(render('``\n`literal`\n``'), /<code> ?`literal` ?<\/code>/);
});

test('asset failure falls back to escaped text', () => {
  for (const options of [{parser:false}, {sanitizer:false}]) {
    const html = renderer(options)('<script>alert(1)</script>\n**text**');
    assert.equal(html, '&lt;script&gt;alert(1)&lt;/script&gt;<br>**text**');
  }
});
