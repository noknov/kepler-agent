import re


def slugify(value: str) -> str:
    """Return a lowercase ASCII-ish slug with single hyphen separators."""
    return re.sub(r"[^a-z0-9]+", "-", value).strip("-")
