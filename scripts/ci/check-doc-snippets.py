#!/usr/bin/env python3
"""Every YAML the site publishes must parse.

The site shows YAML in <app-code lang="yaml"> blocks with a copy button, so a
snippet that does not parse is one a reader pastes into kubectl and gets an
error from. Three shipped broken: a scripted pass over the page collapsed runs
of spaces before a hyphen, which is exactly a YAML sequence item's indentation.

Nothing caught it. The Angular build never reads the string, and a line count
cannot see indentation. An earlier attempt at a hand-written indentation check
in Go produced two false positives on valid documents before being abandoned:
this is the job of a real parser, and CI runners have one.
"""
import pathlib
import re
import sys

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: pip install pyyaml")

PAGES = pathlib.Path(__file__).resolve().parents[2] / "docs" / "src" / "app"
BLOCK = re.compile(r'<app-code lang="yaml">(.*?)</app-code>', re.S)
UNESCAPE = {"&quot;": '"', "&lt;": "<", "&gt;": ">", "&amp;": "&", "&nbsp;": " "}


def main() -> int:
    if not PAGES.is_dir():
        return 0  # nothing to check outside a full checkout
    checked = failed = 0
    for path in sorted(PAGES.rglob("*.ts")):
        for raw in BLOCK.findall(path.read_text(encoding="utf-8")):
            for entity, char in UNESCAPE.items():
                raw = raw.replace(entity, char)
            checked += 1
            try:
                yaml.safe_load(raw)
            except yaml.YAMLError as exc:
                failed += 1
                where = str(exc).splitlines()[0]
                print(f"{path.relative_to(PAGES.parents[2])}: snippet does not parse: {where}")
                print("".join(f"    {line}\n" for line in raw.strip().splitlines()))

    if checked == 0:
        print("no YAML snippet found: the extraction stopped matching, so this check proves nothing")
        return 1
    print(f"{checked} YAML snippet(s) checked, {failed} broken")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
