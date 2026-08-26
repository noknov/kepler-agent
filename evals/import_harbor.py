#!/usr/bin/env python3
"""Fail closed for the retired local Harbor task importer.

Public Harbor datasets include their own container lifecycle and grader. Copying
their task directories into ``evals/run.py`` loses those execution semantics,
so this command intentionally refuses to manufacture a misleading local suite.
"""

import sys


def main() -> int:
    print(
        "The Harbor importer is retired. Run public datasets through "
        "evals/run_harbor.py so Harbor owns task images, isolation, and grading.",
        file=sys.stderr,
    )
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
