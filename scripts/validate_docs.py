#!/usr/bin/env python3
"""验证 WeClaw 上下文文档包。"""

from __future__ import annotations

import argparse
import os
import re
import sys
from pathlib import Path


REQUIRED_FILES = [
    "AGENTS.md",
    "docs/README.md",
    "docs/AI_CONTEXT.md",
    "scripts/validate_docs.py",
]

REQUIRED_HEADINGS = [
    "## Purpose",
    "## Source of truth",
    "## Key facts",
    "## How to verify",
    "## Stale when",
]

SUMMARY_FIELDS = [
    "purpose",
    "read_when",
    "source_of_truth",
    "verify_with",
    "stale_when",
]

GENERIC_PHRASES = [
    "TBD",
    "TODO",
    "Run tests",
    "Check manually",
    "Follow best practices",
    "待定",
    "后续补充",
    "手动检查",
    "遵循最佳实践",
]


def fail(message: str) -> None:
    print(f"文档验证失败：{message}", file=sys.stderr)
    raise SystemExit(1)


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except UnicodeDecodeError as exc:
        fail(f"{path} 不是 UTF-8 文本：{exc}")


def parse_frontmatter(path: Path) -> tuple[dict[str, object], str]:
    text = read_text(path)
    if not text.startswith("---\n"):
        fail(f"{path} 缺少 YAML frontmatter")
    end = text.find("\n---\n", 4)
    if end < 0:
        fail(f"{path} frontmatter 未闭合")
    raw = text[4:end]
    body = text[end + 5 :]
    if raw.count("ai_summary:") != 1:
        fail(f"{path} 必须且只能包含一个 ai_summary")
    return parse_ai_summary(path, raw), body


def parse_ai_summary(path: Path, raw: str) -> dict[str, object]:
    lines = raw.splitlines()
    summary: dict[str, object] = {}
    current: str | None = None
    in_summary = False
    for line in lines:
        if line == "ai_summary:":
            in_summary = True
            continue
        if not in_summary:
            continue
        field_match = re.match(r"^  ([a-z_]+):(?:\s+\"(.*)\")?\s*$", line)
        if field_match:
            current = field_match.group(1)
            value = field_match.group(2)
            summary[current] = value if value is not None else []
            continue
        item_match = re.match(r"^    - \"(.*)\"\s*$", line)
        if item_match and current:
            value = summary.setdefault(current, [])
            if not isinstance(value, list):
                fail(f"{path} 字段 {current} 不能同时是字符串和列表")
            value.append(item_match.group(1))
            continue
        if line.strip():
            fail(f"{path} frontmatter 行无法解析：{line}")
    for field in SUMMARY_FIELDS:
        if field not in summary:
            fail(f"{path} ai_summary 缺少字段 {field}")
        value = summary[field]
        if isinstance(value, str) and not value.strip():
            fail(f"{path} ai_summary.{field} 不能为空")
        if isinstance(value, list) and not any(item.strip() for item in value):
            fail(f"{path} ai_summary.{field} 不能为空列表")
    return summary


def validate_authority_doc(root: Path, rel: str) -> None:
    path = root / rel
    summary, body = parse_frontmatter(path)
    if re.search(r"\nai_summary:", body):
        fail(f"{rel} 正文中存在重复 ai_summary")
    for heading in REQUIRED_HEADINGS:
        if heading not in body:
            fail(f"{rel} 缺少标题 {heading}")
    check_generic_content(rel, body)
    check_source_paths(root, rel, summary["source_of_truth"])
    check_verify_commands(rel, summary["verify_with"])
    check_local_markdown_links(root, rel, body)


def check_generic_content(rel: str, text: str) -> None:
    for phrase in GENERIC_PHRASES:
        if phrase in text:
            fail(f"{rel} 包含占位或泛化内容：{phrase}")


def check_source_paths(root: Path, rel: str, values: object) -> None:
    if not isinstance(values, list):
        fail(f"{rel} source_of_truth 必须是列表")
    for item in values:
        if not isinstance(item, str):
            fail(f"{rel} source_of_truth 存在非字符串项")
        if is_local_absolute_path(item):
            fail(f"{rel} source_of_truth 不能使用本机绝对路径：{item}")
        if item.startswith("http://") or item.startswith("https://"):
            continue
        if not (root / item).exists():
            fail(f"{rel} source_of_truth 路径不存在：{item}")


def check_verify_commands(rel: str, values: object) -> None:
    if not isinstance(values, list):
        fail(f"{rel} verify_with 必须是列表")
    for command in values:
        if not isinstance(command, str) or not command.strip():
            fail(f"{rel} verify_with 存在空命令")
        if any(phrase in command for phrase in GENERIC_PHRASES):
            fail(f"{rel} verify_with 使用了泛化命令：{command}")


def check_local_markdown_links(root: Path, rel: str, body: str) -> None:
    for target in re.findall(r"\[[^\]]+\]\(([^)]+)\)", body):
        if target.startswith(("http://", "https://", "#", "mailto:")):
            continue
        path_part = target.split("#", 1)[0]
        if not path_part or path_part.startswith("/"):
            continue
        base = (root / rel).parent
        if not (base / path_part).exists():
            fail(f"{rel} 包含不存在的本地链接：{target}")


def is_local_absolute_path(value: str) -> bool:
    return value.startswith(("/Users/", "/Volumes/", "/home/")) or re.match(r"^[A-Za-z]:\\", value) is not None


def validate_readme_legacy_section(root: Path) -> None:
    body = parse_frontmatter(root / "docs/README.md")[1]
    if "## Legacy detail docs" not in body:
        fail("docs/README.md 缺少 Legacy detail docs 分区")
    legacy_paths = re.findall(r"`(docs/[^`]+\.md)`", body)
    for rel in legacy_paths:
        if rel in {"docs/README.md", "docs/AI_CONTEXT.md"}:
            continue
        if not (root / rel).exists():
            fail(f"Legacy detail docs 引用了不存在的文件：{rel}")


def validate_repository_shape(root: Path) -> None:
    nested = [p for p in root.glob("*") if (p / ".git").exists()]
    if nested:
        context = read_text(root / "docs/AI_CONTEXT.md")
        if "coordination" not in context and "git -C" not in context:
            fail("检测到嵌套仓库，但上下文文档未说明 coordination directory 或 git -C 验证")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("context_root")
    parser.add_argument("--profile", choices=["generic", "android"], default="generic")
    args = parser.parse_args()
    root = Path(args.context_root).resolve()
    if args.profile == "android":
        fail("当前 WeClaw 上下文包只声明 generic profile")
    for rel in REQUIRED_FILES:
        if not (root / rel).exists():
            fail(f"缺少必需文件：{rel}")
    for rel in ["AGENTS.md", "docs/README.md", "docs/AI_CONTEXT.md"]:
        validate_authority_doc(root, rel)
    validate_readme_legacy_section(root)
    validate_repository_shape(root)
    print("文档验证通过：generic")


if __name__ == "__main__":
    main()
