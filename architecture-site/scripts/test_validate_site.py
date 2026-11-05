"""Regression checks for broken documentation that must fail CI."""
from pathlib import Path
import runpy
import tempfile
import unittest

validate = runpy.run_path(str(Path(__file__).with_name('validate-site.py')))['validate']
VALID = '<main id="content"><h1>Title</h1><section class="doc-section" id="topic"><h2>Topic</h2></section>{}</main>'


class SiteValidationTests(unittest.TestCase):
    def check_page(self, extra='', source_root=None):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'index.html').write_text(VALID.format(extra))
            return validate(root, source_root)[0]

    def test_cross_page_anchor_is_checked(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'index.html').write_text(VALID.format('<a href="other.html#absent">Other</a>'))
            (root / 'other.html').write_text(VALID.format(''))
            self.assertTrue(any('missing anchor' in error for error in validate(root)[0]))

    def test_duplicate_anchor_is_rejected(self):
        self.assertTrue(any('duplicate anchors' in error for error in self.check_page('<p id="topic">Duplicate</p>')))

    def test_missing_asset_is_rejected(self):
        self.assertTrue(any('missing or out-of-site' in error for error in self.check_page('<script src="missing.js"></script>')))

    def test_unpinned_source_is_rejected(self):
        self.assertTrue(any('pin a full commit' in error for error in self.check_page('<a href="https://github.com/noknov/kepler-agent/blob/main/docs.md">Source</a>', Path('/tmp'))))

    def test_valid_anchor_passes(self):
        self.assertEqual(self.check_page('<a href="#topic">Topic</a>'), [])
