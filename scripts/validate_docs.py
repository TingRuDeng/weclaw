#!/usr/bin/env python3
import argparse
import ast
import re
import shlex
import subprocess
import sys
from pathlib import Path
from urllib.parse import unquote

AI_CONTEXT_PATH = Path("docs/AI_CONTEXT.md")
CONTRACT_VERSION = "1.0"
DEFAULT_PROFILE = "generic"
CORE_CONTEXT_DOCS = ("AGENTS.md", "docs/README.md", "docs/AI_CONTEXT.md")
GENERIC_REQUIRED_FILES = CORE_CONTEXT_DOCS + ("scripts/validate_docs.py",)
ROOT_AUTHORITY_FILES = ("AGENTS.md",)
ANDROID_REQUIRED_FILES = (
    "docs/BUILD_MATRIX.md",
    "docs/MODULE_MAP.md",
    "docs/TESTING_MATRIX.md",
    "docs/MANIFEST_AND_PERMISSIONS.md",
)
MAX_FILE_BYTES = 1_000_000
MAX_AI_CONTEXT_LINES = 120
MAX_AGENTS_LINES = 350
PLACEHOLDER_PATTERN = re.compile(
    r"(?<![/\\.\w-])(?:TBD|TODO|FIXME|XXX)(?![/\\.\w-])|待补(?:充)?|后续补充",
    re.I,
)
MACHINE_PATH_PATTERN = re.compile(r"(?<![\w.-])(/Users/|/Volumes/|/home/|[A-Za-z]:\\)")
LINK_PATTERN = re.compile(r"\[[^\]]+\]\(([^)]+)\)")
H2_PATTERN = re.compile(r"^ {0,3}##(?!#)[ \t]+(.+?)[ \t]*#*[ \t]*$")
FENCE_PATTERN = re.compile(r"^ {0,3}(`{3,}|~{3,})")
REQUIRED_AUTHORITY_HEADINGS = (
    "## Purpose",
    "## Source of truth",
    "## Key facts",
    "## How to verify",
    "## Stale when",
)
LEGACY_AUTHORITY_HEADINGS = (
    "## Purpose",
    "## Source Of Truth",
    "## Key Facts",
    "## How To Verify",
    "## Stale When",
)
REQUIRED_AI_KEYS = ("purpose", "read_when", "source_of_truth", "verify_with", "stale_when")
AI_CONTEXT_SECTIONS = (
    "## Project Snapshot",
    "## Core Directories",
    "## Documentation Map",
    "## Common Task Reading Paths",
    "## High-Risk Areas",
    "## Validation Commands",
    "## Stale when",
)
GENERIC_SECTION_VALUES = {
    "tbd",
    "todo",
    "n/a",
    "coming soon",
    "run tests",
    "check manually",
    "follow best practices",
    "use proper architecture",
    "use clean architecture",
    "run appropriate tests",
    "follow conventions",
    "检查一下",
    "手动确认",
    "运行测试",
    "按需验证",
    "遵循最佳实践",
    "后续补充",
    "待补充",
    "人工检查",
    "执行测试",
    "使用合适的验证",
}
KNOWN_EXECUTABLES = {
    "adb",
    "ansible",
    "bash",
    "bazel",
    "buck",
    "bun",
    "bundle",
    "cargo",
    "cmake",
    "composer",
    "curl",
    "dart",
    "deno",
    "docker",
    "docker-compose",
    "dotnet",
    "emulator",
    "fastlane",
    "flutter",
    "gh",
    "git",
    "go",
    "gradle",
    "gradlew",
    "hatch",
    "helm",
    "java",
    "javac",
    "just",
    "kotlinc",
    "kubectl",
    "make",
    "mvn",
    "mvnw",
    "mypy",
    "ninja",
    "node",
    "npm",
    "npx",
    "pdm",
    "perl",
    "php",
    "pip",
    "pip3",
    "pnpm",
    "podman",
    "poetry",
    "pre-commit",
    "pytest",
    "python",
    "python3",
    "rspec",
    "ruby",
    "ruff",
    "rustc",
    "sh",
    "swift",
    "terraform",
    "tofu",
    "tox",
    "uv",
    "wget",
    "xcodebuild",
    "yarn",
    "zsh",
}
SCRIPT_INTERPRETERS = {"bash", "deno", "node", "perl", "php", "python", "python3", "ruby", "sh", "zsh"}
SCRIPT_SUFFIXES = (".bash", ".js", ".mjs", ".php", ".pl", ".py", ".rb", ".sh", ".ts")
SHELL_CONTROL_PATTERN = re.compile(r"(?:&&|\|\||[|;<>`])|\$\(")
VERIFY_TIERS = ("quick", "full", "network-read", "device-required", "release-side-effect")
READ_ONLY_EXTERNAL_PREFIXES = (
    "cargo search",
    "curl ",
    "docker pull",
    "gh pr checks",
    "gh pr view",
    "gh release view",
    "gh run list",
    "gh run view",
    "git fetch",
    "git ls-remote",
    "go list -m",
    "helm list",
    "kubectl get",
    "npm view",
    "pip index versions",
    "pnpm view",
    "terraform plan",
    "tofu plan",
    "wget ",
    "yarn info",
)
RELEASE_SIDE_EFFECT_PATTERNS = (
    re.compile(r"\bgit\s+(?:commit|push|tag)\b"),
    re.compile(
        r"\bgh\s+(?:"
        r"pr\s+(?:close|comment|create|edit|merge|ready|reopen|review)"
        r"|issue\s+(?:close|comment|create|delete|edit|reopen)"
        r"|release\s+(?:create|delete|edit|upload)"
        r"|run\s+(?:cancel|delete|rerun)"
        r"|workflow\s+run"
        r"|repo\s+(?:create|delete|edit|fork|rename|sync)"
        r"|(?:secret|variable)\s+(?:delete|set)"
        r")\b"
    ),
    re.compile(r"\bgh\s+api\b.*(?:--method|-x)\s*(?:delete|patch|post|put)\b"),
    re.compile(r"\b(?:npm|pnpm|cargo)\s+publish\b"),
    re.compile(r"\byarn\s+npm\s+publish\b"),
    re.compile(r"\b(?:mvn|mvnw)\s+deploy\b"),
    re.compile(r"\b(?:gradle|gradlew|\./gradlew)\b.*\bpublish\b"),
    re.compile(r"\bdocker\s+push\b"),
    re.compile(r"\b(?:kubectl|helm)\s+(?:apply|delete|install|upgrade|uninstall)\b"),
    re.compile(r"\b(?:terraform|tofu)\s+(?:apply|destroy|import)\b"),
    re.compile(r"\bxcodebuild\b.*\b(?:archive|-exportarchive)\b"),
    re.compile(r"\b(?:make|just)\s+(?:deploy|publish|release|upload)\b"),
    re.compile(r"\b(?:npm|pnpm|yarn)\s+(?:run\s+)?(?:deploy|publish|release|upload)\b"),
    re.compile(
        r"(?:^|\s)(?:\./)?(?:scripts?/)?(?:deploy|publish|release|upload)"
        r"\.(?:bash|js|mjs|py|rb|sh)(?:\s|$)"
    ),
    re.compile(r"\bcurl\b.*(?:--request|-x)\s*(?:post|put|patch|delete)\b"),
)
DEVICE_REQUIRED_PATTERNS = (
    re.compile(r"\b(?:adb|emulator|simctl)\b"),
    re.compile(r"\bconnected\w*androidtest\b"),
    re.compile(r"\bxcodebuild\b.*\b(?:destination|uitest|test-without-building)\b"),
)
SECRET_PATTERNS = (
    ("private-key", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----")),
    ("aws-access-key", re.compile(r"\bAKIA[0-9A-Z]{16}\b")),
    ("github-token", re.compile(r"\bgh[pousr]_[A-Za-z0-9_]{20,}\b")),
    ("slack-token", re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{20,}\b")),
    ("jwt", re.compile(r"\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b")),
)
SECRET_ASSIGNMENT_PATTERN = re.compile(
    r"""(?im)\b(?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|password)\b"""
    r"""\s*[:=]\s*["']?([A-Za-z0-9_./+=-]{16,})"""
)
SECRET_PLACEHOLDERS = ("changeme", "dummy", "example", "replace", "test", "your_", "xxxxx")
SKIPPED_DOC_PARTS = ("docs/AGENT_STARTER_PROMPT.md",)
SKIPPED_LINK_DIRS = {
    ".agents",
    ".codex",
    ".claude",
    ".cursor",
    ".git",
    ".gradle",
    ".idea",
    ".mypy_cache",
    ".next",
    ".pytest_cache",
    ".ruff_cache",
    ".tox",
    ".venv",
    ".worktrees",
    "DerivedData",
    "Pods",
    "__pycache__",
    "build",
    "coverage",
    "dist",
    "node_modules",
    "out",
    "target",
    "vendor",
    "venv",
}
NON_AUTHORITY_DOC_SECTIONS = (
    "## Detail docs",
    "## Legacy detail docs",
)


class SummaryParseError(ValueError):
    pass


def validate_root(root, profile=DEFAULT_PROFILE):
    base = Path(root).resolve()
    issues = validate_base(base)
    if issues:
        return issues

    non_authority_docs, boundary_issues = non_authority_doc_boundaries(base, profile)
    issues.extend(boundary_issues)
    issues.extend(validate_profile_files(base, profile))
    issues.extend(validate_authority_docs(base, non_authority_docs, profile))
    issues.extend(validate_ai_context(base))
    issues.extend(validate_repository_shape(base))
    issues.extend(validate_links(base))
    issues.extend(validate_secrets(base))
    return issues


def validate_base(base):
    if not base.exists():
        return [f"{base}: 路径不存在"]
    return [] if base.is_dir() else [f"{base}: 必须是目录"]


def validate_profile_files(base, profile):
    if profile not in ("generic", "android"):
        return [f"{base}: 未知 profile {profile}"]

    issues = []
    for rel in required_files_for(profile):
        path = base / rel
        if not path.exists():
            issues.append(f"{rel}: 缺少必需文件 {rel}")
        elif not path.is_file():
            issues.append(f"{rel}: 必需路径必须是文件")
    return issues


def required_files_for(profile):
    return GENERIC_REQUIRED_FILES + (ANDROID_REQUIRED_FILES if profile == "android" else ())


def detect_android_project(root):
    base = Path(root).resolve()
    if not base.is_dir():
        return False

    for path in base.rglob("AndroidManifest.xml"):
        if is_project_input(path, base):
            return True

    plugin_pattern = re.compile(r"\bcom\.android\.(?:application|library)\b")
    gradle_files = list(base.rglob("*.gradle")) + list(base.rglob("*.gradle.kts"))
    for path in gradle_files:
        if not is_project_input(path, base):
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        if plugin_pattern.search(text):
            return True
    return False


def is_project_input(path, base):
    try:
        rel_parts = path.relative_to(base).parts
    except ValueError:
        return False
    return not SKIPPED_LINK_DIRS.intersection(rel_parts) and is_within_repo(path, base)


def validate_authority_docs(base, non_authority_docs=(), profile=DEFAULT_PROFILE):
    issues = []
    required_authority = ROOT_AUTHORITY_FILES + ("docs/README.md",)
    if profile == "android":
        required_authority += ANDROID_REQUIRED_FILES
    required_authority_set = set(required_authority)
    paths = {base / rel for rel in required_authority}
    docs_dir = base / "docs"
    if docs_dir.is_dir():
        paths.update(path for path in markdown_paths(base) if docs_dir in path.parents)

    for path in sorted(paths):
        if not path.exists():
            continue
        rel = relative_path(path, base)
        if not is_within_repo(path, base):
            issues.append(f"{rel}: 文档路径越出仓库根目录")
            continue
        if rel == AI_CONTEXT_PATH.as_posix():
            continue
        if rel not in required_authority_set and should_skip_authority_doc(rel, non_authority_docs):
            continue
        if not path.is_file():
            issues.append(f"{rel}: authority doc 必须是文件")
            continue
        text, read_issues = load_text(path, base)
        issues.extend(read_issues)
        if text is None:
            continue
        issues.extend(validate_file_text(path, base, text))
        issues.extend(validate_authority_contract(path, base, text))
    return issues


def validate_file_text(path, base, text):
    rel = relative_path(path, base)
    issues = []
    if PLACEHOLDER_PATTERN.search(text):
        issues.append(f"{rel}: 存在占位词或未完成标记")
    if len(re.findall(r"(?m)^\s*ai_summary:\s*$", text)) > 1:
        issues.append(f"{rel}: 包含多个 ai_summary 摘要块")
    if MACHINE_PATH_PATTERN.search(text):
        issues.append(f"{rel}: 包含不可移植的本机绝对路径")
    if rel == "AGENTS.md" and len(text.splitlines()) > MAX_AGENTS_LINES:
        issues.append(f"{rel}: 超过 {MAX_AGENTS_LINES} 行路由文件预算")
    if path.stat().st_size > MAX_FILE_BYTES:
        issues.append(f"{rel}: 文件超过 {MAX_FILE_BYTES} 字节")
    return issues


def validate_authority_contract(path, base, text):
    rel = relative_path(path, base)
    issues = []
    headings = authority_headings(text)
    if not headings:
        names = h2_names(text)
        missing = []
        for current, legacy in zip(REQUIRED_AUTHORITY_HEADINGS, LEGACY_AUTHORITY_HEADINGS):
            if heading_name(current) not in names and heading_name(legacy) not in names:
                missing.append(current)
        if missing:
            issues.extend(f"{rel}: 缺少必备标题 {heading}" for heading in missing)
        else:
            issues.append(f"{rel}: 必备标题顺序错误")
    issues.extend(validate_ai_summary(rel, text, base))
    issues.extend(validate_generic_sections(rel, text, headings))
    issues.extend(validate_verify_tiers(rel, text, headings, base))
    return issues


def authority_headings(text):
    names = h2_names(text)
    for candidate in (REQUIRED_AUTHORITY_HEADINGS, LEGACY_AUTHORITY_HEADINGS):
        expected = [heading_name(item) for item in candidate]
        positions = ordered_positions(names, expected)
        if positions and positions == sorted(positions):
            return candidate
    return ()


def ordered_positions(values, expected):
    positions = []
    start = 0
    for item in expected:
        try:
            position = values.index(item, start)
        except ValueError:
            return []
        positions.append(position)
        start = position + 1
    return positions


def heading_name(heading):
    return heading.removeprefix("## ").strip()


def h2_sections(text):
    sections = []
    fence = ""
    offset = 0
    for line in text.splitlines(keepends=True):
        fence_match = FENCE_PATTERN.match(line)
        if fence_match:
            marker = fence_match.group(1)
            if not fence:
                fence = marker[0]
            elif marker[0] == fence:
                fence = ""
            offset += len(line)
            continue
        if not fence:
            heading_match = H2_PATTERN.match(line.rstrip("\r\n"))
            if heading_match:
                sections.append(
                    {
                        "name": heading_match.group(1).strip(),
                        "start": offset,
                        "content_start": offset + len(line),
                    }
                )
        offset += len(line)

    for index, section in enumerate(sections):
        section["end"] = sections[index + 1]["start"] if index + 1 < len(sections) else len(text)
    return sections


def h2_names(text):
    return [section["name"] for section in h2_sections(text)]


def validate_ai_summary(rel, text, base):
    try:
        summary = parse_ai_summary(text)
    except SummaryParseError as exc:
        if str(exc) in ("缺少 frontmatter ai_summary", "必须使用文件首部 frontmatter"):
            return [f"{rel}: 缺少 ai_summary 摘要块"]
        return [f"{rel}: ai_summary 格式错误 {exc}"]

    issues = []
    for key in REQUIRED_AI_KEYS:
        issues.extend(validate_summary_key(rel, key, summary))
    issues.extend(validate_source_paths(rel, summary, base))
    issues.extend(validate_verify_commands(rel, summary, base))
    return issues


def parse_ai_summary(text):
    frontmatter = extract_frontmatter(text)
    lines = frontmatter.splitlines()
    roots = [index for index, line in enumerate(lines) if line == "ai_summary:"]
    if not roots:
        raise SummaryParseError("缺少 frontmatter ai_summary")
    if len(roots) > 1:
        raise SummaryParseError("包含重复 ai_summary")

    data = {}
    current_key = ""
    started = False
    for raw_line in lines[roots[0] + 1 :]:
        if not raw_line.strip():
            continue
        if "\t" in raw_line:
            raise SummaryParseError("不能使用制表符缩进")
        if not raw_line.startswith(" "):
            break
        started = True
        if raw_line.startswith("  ") and not raw_line.startswith("    "):
            match = re.fullmatch(r"  ([a-z_]+):(.*)", raw_line)
            if not match:
                raise SummaryParseError(f"字段缩进或语法无效 {raw_line.strip()}")
            key, raw_value = match.group(1), match.group(2).strip()
            if key not in REQUIRED_AI_KEYS:
                raise SummaryParseError(f"未知字段 {key}")
            if key in data:
                raise SummaryParseError(f"重复字段 {key}")
            current_key = key
            data[key] = parse_summary_value(key, raw_value)
            continue
        if raw_line.startswith("    - "):
            if not current_key or current_key == "purpose":
                raise SummaryParseError("列表项没有对应字段")
            if not isinstance(data.get(current_key), list):
                raise SummaryParseError(f"{current_key} 不能同时使用标量和列表")
            value = clean_value(raw_line[6:])
            if not value:
                raise SummaryParseError(f"{current_key} 包含空列表项")
            data[current_key].append(value)
            continue
        raise SummaryParseError(f"缩进无效 {raw_line.strip()}")

    if not started:
        raise SummaryParseError("ai_summary 不能为空")
    return data


def extract_frontmatter(text):
    lines = text.splitlines()
    if not lines or lines[0].strip() != "---":
        raise SummaryParseError("必须使用文件首部 frontmatter")
    for index, line in enumerate(lines[1:], start=1):
        if line.strip() == "---":
            return "\n".join(lines[1:index])
    raise SummaryParseError("frontmatter 未闭合")


def parse_summary_value(key, raw_value):
    if not raw_value:
        return [] if key != "purpose" else ""
    if raw_value.startswith("["):
        try:
            parsed = ast.literal_eval(raw_value)
        except (SyntaxError, ValueError) as exc:
            raise SummaryParseError(f"{key} inline list 无效") from exc
        if not isinstance(parsed, list) or not all(isinstance(item, str) for item in parsed):
            raise SummaryParseError(f"{key} inline list 必须只包含字符串")
        if any(not item.strip() for item in parsed):
            raise SummaryParseError(f"{key} inline list 包含空项")
        return [item.strip() for item in parsed]
    if key != "purpose":
        raise SummaryParseError(f"{key} 必须使用列表")
    return clean_value(raw_value)


def clean_value(value):
    value = value.strip()
    if len(value) >= 2 and value[0] == value[-1] and value[0] in ("'", '"'):
        try:
            parsed = ast.literal_eval(value)
        except (SyntaxError, ValueError):
            return value[1:-1]
        return str(parsed).strip()
    return value


def validate_summary_key(rel, key, summary):
    value = summary.get(key)
    if key == "purpose":
        return [] if isinstance(value, str) and value.strip() else [f"{rel}: ai_summary.purpose 不能为空"]
    valid = isinstance(value, list) and value and all(isinstance(item, str) and item.strip() for item in value)
    return [] if valid else [f"{rel}: ai_summary.{key} 必须至少包含一项"]


def validate_ai_context(base):
    path = base / AI_CONTEXT_PATH
    if not path.exists() or not path.is_file():
        return []
    if not is_within_repo(path, base):
        return [f"{AI_CONTEXT_PATH.as_posix()}: 文档路径越出仓库根目录"]
    text, read_issues = load_text(path, base)
    if text is None:
        return read_issues
    rel = AI_CONTEXT_PATH.as_posix()
    issues = list(read_issues)
    issues.extend(validate_file_text(path, base, text))
    issues.extend(validate_ai_summary(rel, text, base))
    issues.extend(validate_ai_context_sections(rel, text))
    issues.extend(validate_verify_tiers(rel, text, ("## Validation Commands",), base))
    if len(text.splitlines()) > MAX_AI_CONTEXT_LINES:
        issues.append(f"{rel}: 超过 {MAX_AI_CONTEXT_LINES} 行上下文预算")
    return issues


def validate_ai_context_sections(rel, text):
    names = h2_names(text)
    expected = [heading_name(section) for section in AI_CONTEXT_SECTIONS]
    positions = ordered_positions(names, expected)
    issues = []
    for section in expected:
        if section not in names:
            issues.append(f"{rel}: AI_CONTEXT 缺少章节 ## {section}")
    if not issues and not positions:
        issues.append(f"{rel}: AI_CONTEXT 章节顺序错误")
    return issues


def validate_source_paths(rel, summary, base):
    issues = []
    for entry in summary_entries(summary, "source_of_truth"):
        if entry.startswith(("http://", "https://")):
            continue
        path, error = resolve_repo_path(base, entry)
        if error:
            issues.append(f"{rel}: source_of_truth 路径无效 {entry}: {error}")
        elif not path.exists():
            issues.append(f"{rel}: source_of_truth 路径不存在 {entry}")
    return issues


def resolve_repo_path(base, entry, relative_to=None):
    if not entry or entry.startswith("~") or Path(entry).is_absolute() or re.match(r"^[A-Za-z]:[\\/]", entry):
        return None, "必须使用仓库相对路径"
    parent = relative_to if relative_to is not None else base
    candidate = (parent / entry).resolve()
    try:
        candidate.relative_to(base)
    except ValueError:
        return None, "路径越出仓库根目录"
    return candidate, ""


def validate_verify_commands(rel, summary, base):
    issues = []
    for command in summary_entries(summary, "verify_with"):
        tokens, error = parse_command(command)
        if error:
            issues.append(f"{rel}: verify_with 不是具体命令 {command}: {error}")
            continue
        path_error = validate_command_paths(tokens, base)
        if path_error:
            issues.append(f"{rel}: verify_with 命令路径无效 {command}: {path_error}")
    return issues


def summary_entries(summary, key):
    value = summary.get(key, [])
    if isinstance(value, list):
        return value
    return [value] if isinstance(value, str) and value.strip() else []


def is_specific_command(command):
    _, error = parse_command(command)
    return not error


def parse_command(command):
    stripped = command.strip()
    if not stripped or "\n" in stripped or "\r" in stripped:
        return [], "命令必须是单行"
    if stripped.lower() in GENERIC_SECTION_VALUES:
        return [], "不能使用空泛说明"
    if SHELL_CONTROL_PATTERN.search(stripped):
        return [], "不能包含 shell 控制符"
    try:
        tokens = shlex.split(stripped, posix=True)
    except ValueError as exc:
        return [], f"shell 语法无效 ({exc})"
    if not tokens:
        return [], "命令不能为空"

    executable = tokens[0]
    executable_name = Path(executable).name
    if executable.startswith("-"):
        return [], "缺少可执行程序"
    if (
        executable_name not in KNOWN_EXECUTABLES
        and not executable.startswith(("./", "../"))
        and "/" not in executable
    ):
        return [], f"未知可执行程序 {executable}"
    return tokens, ""


def validate_command_paths(tokens, base):
    executable = tokens[0]
    if is_repo_command_path(executable):
        error = validate_local_command_path(base, executable)
        if error:
            return error

    executable_name = Path(executable).name
    if executable_name not in SCRIPT_INTERPRETERS:
        return ""
    script = interpreter_script(tokens)
    if not script:
        return ""
    return validate_local_command_path(base, script)


def is_repo_command_path(token):
    return token.startswith(("./", "../")) or "/" in token


def interpreter_script(tokens):
    executable = Path(tokens[0]).name
    if executable in ("python", "python3") and "-m" in tokens[1:]:
        return ""
    for token in tokens[1:]:
        if token.startswith("-"):
            continue
        if token.endswith(SCRIPT_SUFFIXES) or token.startswith(("./", "../")) or "/" in token:
            return token
    return ""


def validate_local_command_path(base, entry):
    path, error = resolve_repo_path(base, entry)
    if error:
        return error
    if not path.exists():
        return f"仓库内命令文件不存在 {entry}"
    if not path.is_file():
        return f"仓库内命令路径必须是文件 {entry}"
    return ""


def validate_generic_sections(rel, text, headings):
    issues = []
    for heading in headings:
        content = section_content(text, heading)
        if is_generic_section(content):
            issues.append(f"{rel}: 章节 {heading} 内容过于空泛")
    return issues


def validate_verify_tiers(rel, text, headings, base):
    issues = []
    for heading in headings:
        if "verify" not in heading.lower() and "Validation Commands" not in heading:
            continue
        content = section_content(text, heading)
        if len(command_lines(content)) > 1 and not has_verify_tier(content):
            issues.append(f"{rel}: 章节 {heading} 缺少验证命令分层")
        issues.extend(validate_tier_command_placement(rel, content, base))
    return issues


def command_lines(content):
    return [line for line in map(normalize_command_line, content.splitlines()) if is_specific_command(line)]


def normalize_command_line(line):
    line = line.strip()
    if line.startswith(("```", "~~~")):
        return ""
    line = line.strip("`")
    if line.startswith("- "):
        line = line[2:].strip()
    for tier in VERIFY_TIERS:
        prefix = f"{tier}:"
        if line.lower().startswith(prefix):
            return line[len(prefix) :].strip()
    return line


def has_verify_tier(content):
    return bool(re.search(rf"(?im)^\s*-?\s*({'|'.join(VERIFY_TIERS)})\s*:", content))


def validate_tier_command_placement(rel, content, base):
    issues, current_tier = [], ""
    tiered = has_verify_tier(content)
    for raw_line in content.splitlines():
        current_tier = line_tier(raw_line) or current_tier
        command = normalize_command_line(raw_line)
        if not is_specific_command(command):
            continue
        tokens, _ = parse_command(command)
        path_error = validate_command_paths(tokens, base)
        if path_error:
            issues.append(f"{rel}: 验证命令路径无效 {command}: {path_error}")
        expected_tier = classify_command(command)
        if tiered and not current_tier:
            issues.append(f"{rel}: 验证命令未归入分层 {command}")
            continue
        if expected_tier == "network-read" and current_tier != "network-read":
            issues.append(f"{rel}: 只读外部命令应放入 network-read 分层 {command}")
        elif expected_tier == "device-required" and current_tier != "device-required":
            issues.append(f"{rel}: 设备命令应放入 device-required 分层 {command}")
        elif expected_tier == "release-side-effect" and current_tier != "release-side-effect":
            issues.append(f"{rel}: 有副作用命令应放入 release-side-effect 分层 {command}")
    return issues


def line_tier(line):
    stripped = line.strip().strip("`").lower().removeprefix("- ").strip()
    return next((tier for tier in VERIFY_TIERS if stripped.startswith(f"{tier}:")), "")


def is_read_only_external_command(command):
    return any(command.strip().lower().startswith(prefix) for prefix in READ_ONLY_EXTERNAL_PREFIXES)


def classify_command(command):
    lowered = command.strip().lower()
    if any(pattern.search(lowered) for pattern in RELEASE_SIDE_EFFECT_PATTERNS):
        return "release-side-effect"
    if is_read_only_external_command(lowered):
        return "network-read"
    if any(pattern.search(lowered) for pattern in DEVICE_REQUIRED_PATTERNS):
        return "device-required"
    return "quick"


def validate_repository_shape(base):
    nested = nested_git_repositories(base)
    if len(nested) < 2:
        return []
    texts, issues = [], []
    for rel in CORE_CONTEXT_DOCS:
        path = base / rel
        if not path.is_file():
            continue
        text, read_issues = load_text(path, base)
        issues.extend(read_issues)
        if text is not None:
            texts.append(text)
    combined = "\n".join(texts)
    if not re.search(r"coordination directory|协调目录", combined, re.I):
        issues.append("repository shape: coordination directory 未在核心上下文中说明")
    for repo in nested:
        command = f"git -C {repo} "
        if command not in combined:
            issues.append(f"repository shape: 缺少 {command.strip()} 验证命令")
    return issues


def nested_git_repositories(base):
    return sorted(path.parent.relative_to(base).as_posix() for path in base.glob("*/.git") if path.exists())


def section_content(text, heading):
    target = heading_name(heading)
    sections = h2_sections(text)
    for section in sections:
        if section["name"] == target:
            return text[section["content_start"] : section["end"]].strip()
    return ""


def is_generic_section(content):
    normalized = re.sub(r"[`\s]+", " ", content).strip().lower()
    return normalized in GENERIC_SECTION_VALUES


def validate_links(base):
    issues = []
    for path in markdown_paths(base):
        if not is_within_repo(path, base):
            issues.append(f"{relative_path(path, base)}: 文档路径越出仓库根目录")
            continue
        text, read_issues = load_text(path, base)
        issues.extend(read_issues)
        if text is not None:
            issues.extend(validate_links_in_file(path, base, text))
    return issues


def markdown_paths(base):
    git_paths = git_markdown_paths(base)
    candidates = git_paths if git_paths is not None else sorted(base.rglob("*.md"))
    return [
        path
        for path in candidates
        if not SKIPPED_LINK_DIRS.intersection(path.relative_to(base).parts)
        and (path.is_file() or path.is_symlink())
    ]


def git_markdown_paths(base):
    try:
        top_level = subprocess.run(
            ["git", "-C", str(base), "rev-parse", "--show-toplevel"],
            check=False,
            capture_output=True,
            text=True,
        )
    except OSError:
        return None
    if top_level.returncode != 0:
        return None
    try:
        git_root = Path(top_level.stdout.strip()).resolve()
    except OSError:
        return None
    if git_root != base:
        return None

    selected = subprocess.run(
        ["git", "-C", str(base), "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.md"],
        check=False,
        capture_output=True,
        text=True,
    )
    if selected.returncode != 0:
        return None
    paths = []
    for rel in selected.stdout.split("\0"):
        if not rel:
            continue
        candidate = base / rel
        try:
            candidate.relative_to(base)
        except ValueError:
            continue
        paths.append(candidate)
    return sorted(paths)


def validate_secrets(base):
    issues = []
    for path in markdown_paths(base):
        if not is_within_repo(path, base):
            continue
        text, read_issues = load_text(path, base)
        issues.extend(read_issues)
        if text is None:
            continue
        rel = relative_path(path, base)
        findings = set()
        for line_number, line in enumerate(text.splitlines(), start=1):
            for rule, pattern in SECRET_PATTERNS:
                for match in pattern.finditer(line):
                    if not is_secret_placeholder(match.group(0)):
                        findings.add((line_number, rule))
            for match in SECRET_ASSIGNMENT_PATTERN.finditer(line):
                if not is_secret_placeholder(match.group(1)):
                    findings.add((line_number, "secret-assignment"))
        for line_number, rule in sorted(findings):
            issues.append(f"{rel}: 可能包含敏感信息 rule={rule} line={line_number}")
    return issues


def is_secret_placeholder(value):
    lowered = value.lower()
    return any(marker in lowered for marker in SECRET_PLACEHOLDERS)


def validate_links_in_file(path, base, text):
    issues = []
    for raw_target in LINK_PATTERN.findall(text):
        target = normalize_link_target(raw_target)
        if not target or is_external_or_anchor(target):
            continue
        target_path, error = resolve_repo_path(base, target.split("#", 1)[0], relative_to=path.parent)
        rel = relative_path(path, base)
        if error:
            issues.append(f"{rel}: 本地链接路径无效 {raw_target}: {error}")
        elif not target_path.exists():
            issues.append(f"{rel}: 本地链接不存在 {raw_target}")
    return issues


def normalize_link_target(target):
    target = target.strip()
    if target.startswith("<") and ">" in target:
        target = target[1 : target.index(">")]
    else:
        target = re.sub(r"""\s+(?:"[^"]*"|'[^']*')\s*$""", "", target)
    return unquote(target.strip())


def should_skip_authority_doc(rel, non_authority_docs=()):
    if rel in SKIPPED_DOC_PARTS or rel.startswith("docs/archive/"):
        return True
    return any(
        rel == boundary or (boundary.endswith("/") and rel.startswith(boundary))
        for boundary in non_authority_docs
    )


def non_authority_doc_boundaries(base, profile=DEFAULT_PROFILE):
    readme = base / "docs/README.md"
    if not readme.exists() or not readme.is_file():
        return set(), []
    if not is_within_repo(readme, base):
        return set(), ["docs/README.md: 文档路径越出仓库根目录"]
    text, issues = load_text(readme, base)
    if text is None:
        return set(), issues
    boundaries = set()
    reserved = {"docs/README.md", AI_CONTEXT_PATH.as_posix()}
    if profile == "android":
        reserved.update(ANDROID_REQUIRED_FILES)
    for heading in NON_AUTHORITY_DOC_SECTIONS:
        section = section_content(text, heading)
        for target in LINK_PATTERN.findall(section):
            normalized = normalize_link_target(target).split("#", 1)[0]
            if not normalized or is_external_or_anchor(normalized):
                continue
            path, error = resolve_repo_path(base, normalized, relative_to=readme.parent)
            if error:
                issues.append(
                    f"docs/README.md: non-authority doc 路径无效 {target}: {error}"
                )
                continue
            rel = path.relative_to(base).as_posix()
            if rel == "docs" or not rel.startswith("docs/"):
                continue
            if rel in reserved:
                issues.append(
                    f"docs/README.md: 必需 authority doc 不能分类为 non-authority {rel}"
                )
                continue
            if path.is_dir() or normalized.endswith("/"):
                rel = f"{rel.rstrip('/')}/"
            boundaries.add(rel)
    return boundaries, issues


def is_external_or_anchor(target):
    return target.startswith("#") or "://" in target or target.startswith("mailto:")


def load_text(path, base):
    rel = relative_path(path, base)
    try:
        return path.read_text(encoding="utf-8"), []
    except UnicodeDecodeError:
        return None, [f"{rel}: Markdown 文件必须使用 UTF-8 编码"]
    except OSError as exc:
        return None, [f"{rel}: 无法读取文件 {exc.strerror or exc.__class__.__name__}"]


def relative_path(path, base):
    return path.relative_to(base).as_posix()


def is_within_repo(path, base):
    try:
        path.resolve().relative_to(base)
    except ValueError:
        return False
    return True


def build_parser():
    parser = argparse.ArgumentParser(prog="validate_docs.py", description="Validate a project context pack.")
    parser.add_argument("root", nargs="?", default=".", help="Context-pack root directory.")
    parser.add_argument("--profile", choices=("generic", "android"), default=DEFAULT_PROFILE)
    parser.add_argument("--version", action="version", version=f"%(prog)s contract {CONTRACT_VERSION}")
    return parser


def parse_args(args):
    parsed = build_parser().parse_args(args)
    return Path(parsed.root), parsed.profile


def main(argv=None):
    root, profile = parse_args(sys.argv[1:] if argv is None else argv)
    issues = validate_root(root, profile=profile)
    for issue in issues:
        print(issue)
    return 1 if issues else 0


if __name__ == "__main__":
    raise SystemExit(main())
