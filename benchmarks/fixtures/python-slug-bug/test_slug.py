import unittest

from slug import slugify


class SlugifyTests(unittest.TestCase):
    def test_mixed_case_and_spacing(self):
        self.assertEqual(slugify("Agent Harness V2"), "agent-harness-v2")

    def test_trims_punctuation(self):
        self.assertEqual(slugify("  hello, world!  "), "hello-world")


if __name__ == "__main__":
    unittest.main()
