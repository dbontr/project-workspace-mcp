#!/usr/bin/env python3
"""Structural validation for an explicitly enabled Git knowledge directory.

This precheck does not establish factual accuracy, provenance, or scientific validity.
"""

from __future__ import annotations

import argparse
import datetime as dt
from pathlib import Path
import re
import sys

KIND_BY_DIR = {
    "decisions": "decision",
    "findings": "finding",
    "experiments": "experiment",
    "handoffs": "handoff",
    "sources": "source",
}
REQUIRED_SECTIONS = {
    "decision": ("Context", "Decision", "Alternatives", "Consequences", "Evidence"),
    "finding": ("Claim", "Method", "Evidence", "Limitations"),
    "experiment": ("Question", "Reproduction", "Results", "Interpretation", "Artifacts"),
    "handoff": ("Current state", "Verified", "Blockers", "Next actions"),
    "source": ("Identity", "Locator", "Integrity", "Usage"),
}
ROOT_SECTIONS = {
    "Project.md": ("Purpose", "Scope", "Acceptance criteria", "Authorities"),
    "Context.md": ("Verified state", "In progress", "Blockers", "Next action", "References"),
}
STATUSES = {"proposed", "provisional", "verified", "superseded", "archived"}
IDENTIFIER = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
DATED_NAME = re.compile(r"^\d{4}-\d{2}-\d{2}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$")
KEY_VALUE = re.compile(r"^([a-z][a-z0-9_-]*):\s*(.*?)\s*$")


def sections(markdown: str) -> dict[str, str]:
    result: dict[str, str] = {}
    current: str | None = None
    for line in markdown.splitlines():
        if line.startswith("## "):
            current = line[3:].strip()
            result.setdefault(current, "")
        elif line.startswith("# "):
            current = None
        elif current is not None:
            result[current] += line + "\n"
    return result


def check_sections(markdown: str, required: tuple[str, ...], label: str) -> list[str]:
    found = sections(markdown)
    problems: list[str] = []
    if not re.search(r"(?m)^# \S", markdown):
        problems.append(f"{label}: missing document title")
    for section in required:
        if section not in found:
            problems.append(f"{label}: missing section '## {section}'")
        elif not found[section].strip():
            problems.append(f"{label}: empty section '## {section}'")
    return problems


def parse_frontmatter(body: str, label: str) -> tuple[dict[str, str], str, list[str]]:
    if not body.startswith("---\n"):
        return {}, body, [f"{label}: required YAML frontmatter is missing"]
    boundary = body.find("\n---\n", 4)
    if boundary == -1:
        return {}, body, [f"{label}: frontmatter closing separator is missing"]
    frontmatter = body[4:boundary]
    rest = body[boundary + 5 :]
    metadata: dict[str, str] = {}
    problems: list[str] = []
    for raw in frontmatter.splitlines():
        match = KEY_VALUE.fullmatch(raw)
        if not match:
            problems.append(f"{label}: frontmatter must use flat 'key: value' syntax")
            continue
        key, value = match.groups()
        if key in metadata:
            problems.append(f"{label}: duplicate frontmatter key '{key}'")
        metadata[key] = value.strip('"\'')
    return metadata, rest, problems


def check_directory(root: Path, strict: bool = False) -> list[str]:
    if not root.exists():
        return [f"{root}: knowledge directory does not exist"] if strict else []
    if not root.is_dir():
        return [f"{root}: knowledge path is not a directory"]
    problems: list[str] = []
    seen: dict[str, str] = {}
    for filename, headings in ROOT_SECTIONS.items():
        path = root / filename
        if not path.exists():
            if strict:
                problems.append(f"{path}: required canonical document is missing")
            continue
        problems += check_sections(path.read_text(encoding="utf-8"), headings, str(path))
    for directory, expected_kind in KIND_BY_DIR.items():
        folder = root / directory
        if not folder.exists():
            continue
        if not folder.is_dir():
            problems.append(f"{folder}: record path is not a directory")
            continue
        for path in sorted(folder.rglob("*")):
            if not path.is_file():
                continue
            if path.suffix != ".md" or path.parent != folder:
                problems.append(f"{path}: records must be direct Markdown children of {directory}/")
                continue
            if directory != "sources" and not DATED_NAME.fullmatch(path.name):
                problems.append(f"{path}: record filename must be YYYY-MM-DD-short-slug.md")
            body = path.read_text(encoding="utf-8")
            meta, content, errors = parse_frontmatter(body, str(path))
            problems += errors
            for field in ("id", "kind", "status", "date"):
                if not meta.get(field):
                    problems.append(f"{path}: missing '{field}'")
            identifier = meta.get("id", "")
            if identifier and not IDENTIFIER.fullmatch(identifier):
                problems.append(f"{path}: invalid stable record ID")
            if identifier in seen:
                problems.append(f"{path}: duplicate record ID also used by {seen[identifier]}")
            elif identifier:
                seen[identifier] = str(path)
            if meta.get("kind") and meta["kind"] != expected_kind:
                problems.append(f"{path}: kind must be '{expected_kind}'")
            if meta.get("status") and meta["status"] not in STATUSES:
                problems.append(f"{path}: invalid record status")
            for date_field in ("date", "updated"):
                if meta.get(date_field):
                    try:
                        dt.date.fromisoformat(meta[date_field])
                    except ValueError:
                        problems.append(f"{path}: {date_field} must be an ISO date")
            problems += check_sections(content, REQUIRED_SECTIONS[expected_kind], str(path))
    return problems


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path, help="Approved knowledge root (often ./knowledge)")
    parser.add_argument("--strict", action="store_true", help="Require Project.md and Context.md")
    args = parser.parse_args()
    problems = check_directory(args.directory, args.strict)
    for problem in problems:
        print("ERROR:", problem, file=sys.stderr)
    if problems:
        print(f"Knowledge structure failed: {len(problems)} issue(s).", file=sys.stderr)
        return 1
    print("Knowledge structure: OK (structural checks only; evidence not verified).")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
