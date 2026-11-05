#!/usr/bin/env python3
"""Validate static site assets, anchors, pinned source links and search index."""
from html.parser import HTMLParser
from pathlib import Path
import re
import runpy
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[1]


class Page(HTMLParser):
    def __init__(self):
        super().__init__()
        self.ids = []
        self.links = []
        self.headings = 0
        self.sections = 0
        self.has_main = False

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if 'id' in attrs:
            self.ids.append(attrs['id'])
        if tag in ('a', 'link', 'script'):
            value = attrs.get('href', attrs.get('src'))
            if value:
                self.links.append(value)
        if tag == 'h1':
            self.headings += 1
        if tag == 'main' and attrs.get('id') == 'content':
            self.has_main = True
        if tag == 'section' and 'doc-section' in attrs.get('class', '').split():
            self.sections += 1
            if not attrs.get('id'):
                raise ValueError('A document section has no anchor')


def validate(root, source_root=None):
    errors = []
    pages = {}
    for path in sorted(root.glob('*.html')):
        parser = Page()
        try:
            parser.feed(path.read_text())
        except ValueError as error:
            errors.append(f'{path.name}: {error}')
        pages[path.name] = parser
        if parser.headings != 1 or not parser.has_main or not parser.sections:
            errors.append(f'{path.name}: requires one h1, a main#content and document sections')
        if len(parser.ids) != len(set(parser.ids)):
            errors.append(f'{path.name}: duplicate anchors')
    if 'index.html' not in pages:
        errors.append('Missing index.html')
    for name, parser in pages.items():
        for link in parser.links:
            url = urlsplit(link)
            if url.scheme or url.netloc:
                if source_root and url.netloc == 'github.com' and url.path.startswith('/noknov/kepler-agent/blob/'):
                    match = re.fullmatch(r'/noknov/kepler-agent/blob/([0-9a-f]{40})/(.+)', unquote(url.path))
                    if not match:
                        errors.append(f'{name}: source links must pin a full commit: {link}')
                    elif not (source_root / match[2]).exists():
                        errors.append(f'{name}: source path does not exist: {match[2]}')
                continue
            target = (root / (unquote(url.path) or name)).resolve()
            if not target.is_relative_to(root.resolve()) or not target.is_file():
                errors.append(f'{name}: missing or out-of-site target: {link}')
            elif url.fragment and target.name in pages and unquote(url.fragment) not in pages[target.name].ids:
                errors.append(f'{name}: missing anchor: {link}')
    return errors, len(pages)


if __name__ == '__main__':
    errors, count = validate(ROOT, ROOT.parent)
    generator = runpy.run_path(str(ROOT / 'scripts/build-search-index.py'))
    index = ROOT / 'assets/search-index.js'
    if not index.exists() or index.read_text() != generator['build']():
        errors.append('Search index is stale; run scripts/build-search-index.py')
    if errors:
        raise SystemExit('\n'.join(errors))
    print(f'Validated {count} pages: structure, assets, anchors, source paths and search index.')
