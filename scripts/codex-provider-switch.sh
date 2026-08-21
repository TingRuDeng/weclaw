#!/usr/bin/env bash

set -euo pipefail
umask 077

usage() {
  cat <<'EOF'
用法:
  codex-provider-switch.sh <openai|OpenAI> [--repair-item-ids] [--apply] [--codex-home PATH]
  codex-provider-switch.sh --restore BACKUP_DIR [--apply] [--codex-home PATH]

默认只做只读预览；只有显式传入 --apply 才会写入。
归档会话不会切换 provider，也不会修复历史 item ID。

选项:
  --repair-item-ids  将已知 Responses 类型的错误 item_ 前缀改为类型专用前缀
  --apply            执行写入；省略时只预览
  --codex-home PATH  指定 Codex 数据目录，默认使用 CODEX_HOME 或 ~/.codex
  --restore DIR      预览或恢复本脚本生成的备份目录
EOF
}

target_provider=""
target_home="${CODEX_HOME:-${HOME}/.codex}"
apply_mode="false"
repair_item_ids="false"
restore_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    openai|OpenAI)
      if [[ -n "${target_provider}" ]]; then
        echo "错误：provider 只能指定一次" >&2
        usage >&2
        exit 2
      fi
      target_provider="$1"
      shift
      ;;
    --apply)
      apply_mode="true"
      shift
      ;;
    --repair-item-ids)
      repair_item_ids="true"
      shift
      ;;
    --codex-home)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "错误：--codex-home 必须跟一个路径" >&2
        exit 2
      fi
      target_home="$2"
      shift 2
      ;;
    --restore)
      if [[ $# -lt 2 || -z "$2" ]]; then
        echo "错误：--restore 必须跟一个备份目录" >&2
        exit 2
      fi
      restore_dir="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "错误：不支持的参数：$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -n "${restore_dir}" ]]; then
  if [[ -n "${target_provider}" || "${repair_item_ids}" == "true" ]]; then
    echo "错误：--restore 不能与 provider 或 --repair-item-ids 同时使用" >&2
    exit 2
  fi
elif [[ "${target_provider}" != "openai" && "${target_provider}" != "OpenAI" ]]; then
  echo "错误：provider 必须精确写成 openai 或 OpenAI" >&2
  usage >&2
  exit 2
fi

command -v python3 >/dev/null 2>&1 || {
  echo "错误：缺少 python3" >&2
  exit 1
}

python3 - "${target_home}" "${target_provider}" "${apply_mode}" "${repair_item_ids}" "${restore_dir}" <<'PY'
import hashlib
import json
import os
import pathlib
import re
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
from datetime import datetime, timezone


ITEM_PREFIXES = {
    "additional_tools": "at",
    "agent_message": "amsg",
    "compaction": "cmp",
    "context_compaction": "cmp",
    "custom_tool_call": "ctc",
    "custom_tool_call_output": "ctco",
    "function_call": "fc",
    "function_call_output": "fco",
    "image_generation_call": "ig",
    "local_shell_call": "lsh",
    "message": "msg",
    "reasoning": "rs",
    "tool_search_call": "tsc",
    "tool_search_output": "tso",
    "web_search_call": "ws",
}
LAST_ERROR_MESSAGE = ""


def fail(message: str) -> None:
    global LAST_ERROR_MESSAGE
    LAST_ERROR_MESSAGE = message
    print(f"错误：{message}", file=sys.stderr)
    raise SystemExit(1)


def table_has_column(connection: sqlite3.Connection, table: str, column: str) -> bool:
    rows = connection.execute(f"PRAGMA table_info({table})").fetchall()
    return any(row[1] == column for row in rows)


def provider_change_filter(
    table: str, target: str, archived_thread_ids: set[str]
) -> tuple[str, tuple[str, ...]]:
    clause = "model_provider IS NULL OR model_provider <> ?"
    parameters: tuple[str, ...] = (target,)
    if table == "threads":
        clause = f"({clause}) AND archived = 0"
    elif table == "local_thread_catalog" and archived_thread_ids:
        placeholders = ",".join("?" for _ in archived_thread_ids)
        clause = f"({clause}) AND thread_id NOT IN ({placeholders})"
        parameters += tuple(sorted(archived_thread_ids))
    return clause, parameters


def count_sqlite_changes(
    path: pathlib.Path,
    table: str,
    target: str,
    optional: bool,
    archived_thread_ids: set[str],
) -> int:
    if not path.exists():
        if optional:
            return 0
        fail(f"required SQLite database does not exist: {path}")
    if path.is_symlink() or not path.is_file():
        fail(f"SQLite path must be a regular file: {path}")
    try:
        connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
        try:
            if not table_has_column(connection, table, "model_provider"):
                if optional:
                    return 0
                fail(f"{path} does not contain {table}.model_provider")
            clause, parameters = provider_change_filter(table, target, archived_thread_ids)
            row = connection.execute(
                f"SELECT COUNT(*) FROM {table} WHERE {clause}", parameters
            ).fetchone()
            return int(row[0])
        finally:
            connection.close()
    except sqlite3.Error as exc:
        fail(f"cannot inspect SQLite database {path}: {exc}")


def sqlite_table_available(path: pathlib.Path, table: str) -> bool:
    if not path.exists() or path.is_symlink() or not path.is_file():
        return False
    try:
        connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
        try:
            return table_has_column(connection, table, "model_provider")
        finally:
            connection.close()
    except sqlite3.Error as exc:
        fail(f"cannot inspect SQLite database {path}: {exc}")


def read_archived_thread_ids(path: pathlib.Path) -> set[str]:
    if not path.exists() or path.is_symlink() or not path.is_file():
        fail(f"required SQLite database does not exist or is unsafe: {path}")
    try:
        connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
        try:
            if not table_has_column(connection, "threads", "archived"):
                fail(f"{path} does not contain threads.archived")
            return {
                row[0]
                for row in connection.execute("SELECT id FROM threads WHERE archived <> 0")
                if isinstance(row[0], str) and row[0]
            }
        finally:
            connection.close()
    except sqlite3.Error as exc:
        fail(f"cannot inspect archived threads in {path}: {exc}")


def contains_encrypted_content(value: object) -> bool:
    if isinstance(value, dict):
        if value.get("encrypted_content"):
            return True
        return any(contains_encrypted_content(child) for child in value.values())
    if isinstance(value, list):
        return any(contains_encrypted_content(child) for child in value)
    return False


def rollout_paths(home: pathlib.Path) -> list[pathlib.Path]:
    directory = home / "sessions"
    if not directory.exists():
        return []
    if directory.is_symlink() or not directory.is_dir():
        fail(f"rollout directory must be a real directory: {directory}")
    return sorted(path for path in directory.rglob("*.jsonl") if path.is_file())


def replace_item_references(value: object, replacements: dict[str, str], key: str = "") -> object:
    if isinstance(value, dict):
        return {
            child_key: replace_item_references(child, replacements, child_key)
            for child_key, child in value.items()
        }
    if isinstance(value, list):
        return [replace_item_references(child, replacements, key) for child in value]
    if isinstance(value, str) and (key == "item_id" or key.endswith("_item_id")):
        return replacements.get(value, value)
    return value


def transform_rollout(
    path: pathlib.Path, target: str, repair_ids: bool
) -> tuple[bytes, int, int, int, int]:
    if path.is_symlink():
        fail(f"rollout must not be a symbolic link: {path}")
    try:
        original = path.read_bytes()
        text = original.decode("utf-8")
    except (OSError, UnicodeDecodeError) as exc:
        fail(f"cannot inspect rollout {path}: {exc}")

    records: list[tuple[dict[str, object] | None, str]] = []
    existing_item_ids: set[str] = set()
    replacements: dict[str, str] = {}
    encrypted_items = 0
    unknown_item_ids = 0

    lines = text.split("\n")
    has_final_newline = text.endswith("\n")
    if has_final_newline:
        lines.pop()

    for line_number, line in enumerate(lines, start=1):
        raw_line = line
        if line_number < len(lines) or has_final_newline:
            raw_line += "\n"
        content = raw_line.rstrip("\r\n")
        if not content.strip():
            records.append((None, raw_line))
            continue
        try:
            record = json.loads(content)
        except json.JSONDecodeError as exc:
            fail(f"invalid JSON in {path}:{line_number}: {exc.msg}")
        if not isinstance(record, dict):
            fail(f"JSONL record must be an object in {path}:{line_number}")
        records.append((record, raw_line))

        payload = record.get("payload")
        if record.get("type") == "session_meta" and not isinstance(payload, dict):
            fail(f"session_meta payload must be an object in {path}:{line_number}")
        if record.get("type") != "response_item" or not isinstance(payload, dict):
            continue
        if contains_encrypted_content(payload):
            encrypted_items += 1
        item_id = payload.get("id")
        if isinstance(item_id, str):
            existing_item_ids.add(item_id)
        if not repair_ids or not isinstance(item_id, str) or not item_id.startswith("item_"):
            continue
        prefix = ITEM_PREFIXES.get(payload.get("type"))
        if not prefix:
            unknown_item_ids += 1
            continue
        replacement = f"{prefix}_{item_id.removeprefix('item_')}"
        previous = replacements.get(item_id)
        if previous is not None and previous != replacement:
            fail(f"one item ID maps to multiple response types in {path}: {item_id}")
        replacements[item_id] = replacement

    for original_id, replacement_id in replacements.items():
        if replacement_id in existing_item_ids and replacement_id != original_id:
            fail(f"item ID repair would collide in {path}: {replacement_id}")

    session_meta_changes = 0
    changed_lines: list[str] = []
    for record, raw_line in records:
        if record is None:
            changed_lines.append(raw_line)
            continue
        changed = False
        payload = record.get("payload")
        if record.get("type") == "session_meta" and isinstance(payload, dict):
            if payload.get("model_provider") != target:
                payload["model_provider"] = target
                session_meta_changes += 1
                changed = True
        if replacements:
            replaced_record = replace_item_references(record, replacements)
            if replaced_record != record:
                record = replaced_record
                payload = record.get("payload")
                changed = True
            if record.get("type") == "response_item" and isinstance(payload, dict):
                item_id = payload.get("id")
                if isinstance(item_id, str) and item_id in replacements:
                    payload["id"] = replacements[item_id]
                    changed = True
        if changed:
            newline = "\r\n" if raw_line.endswith("\r\n") else "\n" if raw_line.endswith("\n") else ""
            changed_lines.append(
                json.dumps(record, ensure_ascii=False, separators=(",", ":")) + newline
            )
        else:
            changed_lines.append(raw_line)

    transformed = "".join(changed_lines).encode("utf-8")
    return transformed, session_meta_changes, len(replacements), encrypted_items, unknown_item_ids


def inspect_rollouts(
    paths: list[pathlib.Path], target: str, repair_ids: bool
) -> tuple[dict[pathlib.Path, bytes], int, int, int, int]:
    transformed_files: dict[pathlib.Path, bytes] = {}
    session_meta_changes = 0
    item_id_repairs = 0
    encrypted_items = 0
    unknown_item_ids = 0
    for path in paths:
        transformed, meta_count, repair_count, encrypted_count, unknown_count = transform_rollout(
            path, target, repair_ids
        )
        if transformed != path.read_bytes():
            transformed_files[path] = transformed
        session_meta_changes += meta_count
        item_id_repairs += repair_count
        encrypted_items += encrypted_count
        unknown_item_ids += unknown_count
    return transformed_files, session_meta_changes, item_id_repairs, encrypted_items, unknown_item_ids


def sha256_file(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def fsync_directory(path: pathlib.Path) -> None:
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def append_operation_log(backup_dir: pathlib.Path, status: str, detail: str = "") -> None:
    log_path = backup_dir / "operation.log"
    with log_path.open("a", encoding="utf-8") as handle:
        handle.write(f"timestamp={datetime.now(timezone.utc).isoformat()} status={status}\n")
        if detail:
            handle.write(f"detail={detail.replace(chr(10), ' ')}\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(log_path, 0o600)


def sqlite_backup(source: pathlib.Path, destination: pathlib.Path) -> None:
    destination.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    source_connection = sqlite3.connect(f"file:{source}?mode=ro", uri=True)
    destination_connection = sqlite3.connect(destination)
    try:
        source_connection.backup(destination_connection)
        result = destination_connection.execute("PRAGMA integrity_check").fetchone()
        if not result or result[0] != "ok":
            fail(f"SQLite backup integrity check failed: {source}")
    finally:
        destination_connection.close()
        source_connection.close()
    os.chmod(destination, 0o600)


def assert_sqlite_writable(path: pathlib.Path) -> None:
    try:
        connection = sqlite3.connect(path, timeout=0)
        try:
            connection.execute("BEGIN IMMEDIATE")
            connection.rollback()
        finally:
            connection.close()
    except sqlite3.Error as exc:
        fail(f"active SQLite writer detected for {path}: {exc}")


def assert_no_open_files(home: pathlib.Path, targets: list[pathlib.Path]) -> None:
    configured_home = pathlib.Path(
        os.environ.get("CODEX_HOME", str(pathlib.Path.home() / ".codex"))
    ).expanduser().resolve()
    process_result = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        check=False,
        capture_output=True,
        text=True,
    )
    if process_result.returncode != 0:
        detail = process_result.stderr.strip() or f"exit status {process_result.returncode}"
        fail(f"process writer check failed: {detail}")
    active_pids: list[str] = []
    home_variants = {str(home)}
    if str(home).startswith("/private/"):
        home_variants.add(str(home)[len("/private") :])
    for raw_line in process_result.stdout.splitlines():
        stripped = raw_line.strip()
        if not stripped:
            continue
        parts = stripped.split(None, 1)
        if len(parts) != 2 or not parts[0].isdigit():
            continue
        pid, command = parts
        normalized_command = re.sub(r"/{2,}", "/", command)
        is_writer_process = bool(
            re.search(r"(?:^|/)codex(?:\s|$)", normalized_command)
            or "/Codex.app/Contents/MacOS/Codex" in normalized_command
            or re.search(r"(?:^|/)weclaw\s+(?:start|restart|codex)(?:\s|$)", normalized_command)
        )
        if not is_writer_process:
            continue
        if home == configured_home or any(
            variant in normalized_command for variant in home_variants
        ):
            active_pids.append(pid)
    if active_pids:
        pids = ",".join(sorted(set(active_pids)))
        fail(f"active Codex/WeClaw process detected; stop it before applying (pid={pids})")

    lsof = shutil.which("lsof")
    if lsof is None:
        fail("lsof is required for --apply so active Codex writers can be detected")
    inspected_paths = list(targets)
    for target in targets:
        if target.suffix == ".sqlite":
            inspected_paths.extend(
                pathlib.Path(f"{target}{suffix}") for suffix in ("-wal", "-shm")
            )
    existing_paths = sorted({str(path) for path in inspected_paths if path.exists()})
    result = subprocess.run(
        [lsof, "-t", "--", *existing_paths],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode == 0 and result.stdout.strip():
        pids = ",".join(sorted(set(result.stdout.split())))
        fail(f"active process has migration target files open; close Codex first (pid={pids})")
    if result.returncode not in (0, 1):
        detail = result.stderr.strip() or f"exit status {result.returncode}"
        fail(f"lsof writer check failed: {detail}")


def ensure_disk_space(home: pathlib.Path, files: list[pathlib.Path]) -> None:
    source_bytes = sum(path.stat().st_size for path in files)
    required_bytes = max(1024 * 1024, source_bytes * 3)
    free_bytes = shutil.disk_usage(home).free
    if free_bytes < required_bytes:
        fail(
            f"insufficient free disk space: need {required_bytes} bytes, have {free_bytes} bytes"
        )


def assert_owned_file_inside_home(home: pathlib.Path, path: pathlib.Path) -> None:
    if path.is_symlink() or not path.is_file():
        fail(f"target must be a regular file: {path}")
    try:
        path.resolve().relative_to(home)
    except ValueError:
        fail(f"target resolves outside Codex home: {path}")
    if path.stat().st_uid != os.getuid():
        fail(f"target must be owned by the current user: {path}")


def make_backup(
    home: pathlib.Path, files: list[pathlib.Path], operation: str, provider: str
) -> pathlib.Path:
    backup_root = home / "backups"
    if backup_root.exists():
        if backup_root.is_symlink() or not backup_root.is_dir():
            fail(f"backup root must be a real directory: {backup_root}")
        if backup_root.stat().st_uid != os.getuid():
            fail(f"backup root must be owned by the current user: {backup_root}")
    else:
        backup_root.mkdir(mode=0o700, parents=True)
    os.chmod(backup_root, 0o700)
    timestamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")
    backup_dir = backup_root / f"provider-switch-{timestamp}-{os.getpid()}"
    backup_dir.mkdir(mode=0o700)
    entries: list[dict[str, object]] = []
    for source in files:
        relative = source.relative_to(home)
        destination = backup_dir / relative
        destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if source.suffix in (".sqlite", ".db"):
            sqlite_backup(source, destination)
            kind = "sqlite"
        else:
            shutil.copy2(source, destination)
            os.chmod(destination, 0o600)
            kind = "jsonl"
        entries.append(
            {
                "path": relative.as_posix(),
                "kind": kind,
                "source_sha256": sha256_file(source),
                "backup_sha256": sha256_file(destination),
                "source_mode": stat.S_IMODE(source.stat().st_mode),
            }
        )
    manifest = {
        "tool": "codex-provider-switch",
        "version": 1,
        "created_at": datetime.now(timezone.utc).isoformat(),
        "operation": operation,
        "target_provider": provider,
        "codex_home": str(home),
        "backup_verified": True,
        "files": entries,
    }
    manifest_path = backup_dir / "manifest.json"
    with manifest_path.open("x", encoding="utf-8") as handle:
        json.dump(manifest, handle, ensure_ascii=False, indent=2, sort_keys=True)
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(manifest_path, 0o600)
    append_operation_log(backup_dir, "backup-verified")
    fsync_directory(backup_dir)
    return backup_dir


def stage_sqlite_update(
    source: pathlib.Path,
    destination: pathlib.Path,
    table: str,
    provider: str,
    archived_thread_ids: set[str],
) -> None:
    sqlite_backup(source, destination)
    connection = sqlite3.connect(destination)
    try:
        clause, filter_parameters = provider_change_filter(
            table, provider, archived_thread_ids
        )
        connection.execute(
            f"UPDATE {table} SET model_provider = ? WHERE {clause}",
            (provider, *filter_parameters),
        )
        connection.commit()
        result = connection.execute("PRAGMA integrity_check").fetchone()
        if not result or result[0] != "ok":
            fail(f"staged SQLite integrity check failed: {source}")
    finally:
        connection.close()


def validate_sqlite_provider(
    path: pathlib.Path, table: str, provider: str, archived_thread_ids: set[str]
) -> None:
    connection = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        result = connection.execute("PRAGMA integrity_check").fetchone()
        if not result or result[0] != "ok":
            fail(f"SQLite integrity check failed: {path}")
        clause, parameters = provider_change_filter(table, provider, archived_thread_ids)
        mismatch = connection.execute(
            f"SELECT COUNT(*) FROM {table} WHERE {clause}", parameters
        ).fetchone()
        if not mismatch or int(mismatch[0]) != 0:
            fail(f"provider verification failed for {path}")
    finally:
        connection.close()


def write_staged_file(path: pathlib.Path, content: bytes, mode: int) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with path.open("xb") as handle:
        handle.write(content)
        handle.flush()
        os.fsync(handle.fileno())
    os.chmod(path, mode)


def replace_from_file(
    source: pathlib.Path,
    destination: pathlib.Path,
    mode: int,
    remove_sqlite_sidecars: bool,
) -> None:
    temporary = destination.parent / f".{destination.name}.provider-switch-{os.getpid()}"
    if temporary.exists():
        fail(f"refusing to overwrite unexpected staging path: {temporary}")
    shutil.copy2(source, temporary)
    os.chmod(temporary, mode)
    with temporary.open("rb") as handle:
        os.fsync(handle.fileno())
    os.replace(temporary, destination)
    if remove_sqlite_sidecars:
        for suffix in ("-wal", "-shm"):
            sidecar = pathlib.Path(f"{destination}{suffix}")
            if sidecar.exists():
                sidecar.unlink()
    fsync_directory(destination.parent)


def apply_switch(
    home: pathlib.Path,
    provider: str,
    database_plans: list[tuple[pathlib.Path, str, int]],
    rollout_plans: dict[pathlib.Path, bytes],
    archived_thread_ids: set[str],
) -> pathlib.Path | None:
    files = [path for path, _, count in database_plans if count] + list(rollout_plans)
    if not files:
        return None
    if home.stat().st_uid != os.getuid():
        fail(f"Codex home must be owned by the current user: {home}")
    for path in files:
        assert_owned_file_inside_home(home, path)
    assert_no_open_files(home, files)
    for path, _, count in database_plans:
        if count:
            assert_sqlite_writable(path)
    ensure_disk_space(home, files)
    backup_dir = make_backup(home, files, "switch", provider)
    stage_root = pathlib.Path(tempfile.mkdtemp(prefix=".stage-", dir=backup_dir))
    replacements: list[tuple[pathlib.Path, pathlib.Path, int, bool]] = []
    try:
        for source, table, count in database_plans:
            if not count:
                continue
            staged = stage_root / source.relative_to(home)
            staged.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            stage_sqlite_update(source, staged, table, provider, archived_thread_ids)
            replacements.append((staged, source, stat.S_IMODE(source.stat().st_mode), True))
        for source, content in rollout_plans.items():
            staged = stage_root / source.relative_to(home)
            mode = stat.S_IMODE(source.stat().st_mode)
            write_staged_file(staged, content, mode)
            replacements.append((staged, source, mode, False))

        manifest = json.loads((backup_dir / "manifest.json").read_text(encoding="utf-8"))
        manifest_entries = {entry["path"]: entry for entry in manifest["files"]}
        for _, destination, _, _ in replacements:
            relative = destination.relative_to(home).as_posix()
            expected = manifest_entries[relative]["source_sha256"]
            if sha256_file(destination) != expected:
                fail(f"source changed after backup; refusing to apply: {destination}")
        assert_no_open_files(home, files)
        for path, _, count in database_plans:
            if count:
                assert_sqlite_writable(path)

        replaced: list[pathlib.Path] = []
        try:
            for staged, destination, mode, is_sqlite in replacements:
                replace_from_file(staged, destination, mode, is_sqlite)
                replaced.append(destination)
            for path, table, count in database_plans:
                if count:
                    validate_sqlite_provider(path, table, provider, archived_thread_ids)
            for path, expected_content in rollout_plans.items():
                validate_jsonl(path)
                if path.read_bytes() != expected_content:
                    fail(f"rollout verification failed after apply: {path}")
        except BaseException:
            for destination in reversed(replaced):
                relative = destination.relative_to(home)
                entry = manifest_entries[relative.as_posix()]
                replace_from_file(
                    backup_dir / relative,
                    destination,
                    int(entry["source_mode"]),
                    entry["kind"] == "sqlite",
                )
            raise
    except BaseException as exc:
        detail = LAST_ERROR_MESSAGE or f"{type(exc).__name__}: {exc}"
        try:
            append_operation_log(backup_dir, "failed", detail)
        except OSError:
            pass
        raise
    else:
        append_operation_log(backup_dir, "applied-and-verified")
    finally:
        shutil.rmtree(stage_root, ignore_errors=True)
    return backup_dir


def validate_jsonl(path: pathlib.Path) -> None:
    try:
        with path.open("r", encoding="utf-8") as handle:
            for line_number, raw_line in enumerate(handle, start=1):
                if not raw_line.strip():
                    continue
                value = json.loads(raw_line)
                if not isinstance(value, dict):
                    fail(f"JSONL record must be an object in {path}:{line_number}")
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        fail(f"invalid rollout backup {path}: {exc}")


def load_restore_manifest(
    backup_argument: str, home: pathlib.Path
) -> tuple[pathlib.Path, dict[str, object], list[tuple[pathlib.Path, pathlib.Path, dict[str, object]]]]:
    unresolved = pathlib.Path(backup_argument).expanduser()
    if unresolved.is_symlink():
        fail(f"backup directory must not be a symbolic link: {unresolved}")
    backup_dir = unresolved.resolve()
    if not backup_dir.is_dir():
        fail(f"backup directory does not exist: {backup_dir}")
    backup_root = home / "backups"
    if backup_root.is_symlink() or not backup_root.is_dir():
        fail(f"backup root must be a real directory: {backup_root}")
    try:
        backup_dir.relative_to(backup_root.resolve())
    except ValueError:
        fail(f"backup directory must be inside {backup_root}")
    manifest_path = backup_dir / "manifest.json"
    if manifest_path.is_symlink() or not manifest_path.is_file():
        fail(f"backup manifest is missing or unsafe: {manifest_path}")
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        fail(f"cannot read backup manifest {manifest_path}: {exc}")
    if not isinstance(manifest, dict):
        fail("backup manifest must be a JSON object")
    if manifest.get("tool") != "codex-provider-switch" or manifest.get("version") != 1:
        fail("backup manifest was not created by this script version")
    if manifest.get("codex_home") != str(home):
        fail(
            f"backup belongs to a different Codex home: {manifest.get('codex_home')!r}"
        )
    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        fail("backup manifest contains no files")

    resolved_entries: list[tuple[pathlib.Path, pathlib.Path, dict[str, object]]] = []
    seen_paths: set[str] = set()
    for entry in entries:
        if not isinstance(entry, dict):
            fail("backup manifest file entries must be objects")
        relative_text = entry.get("path")
        if not isinstance(relative_text, str) or not relative_text:
            fail("backup manifest contains an invalid file path")
        relative = pathlib.PurePosixPath(relative_text)
        if relative.is_absolute() or ".." in relative.parts or relative_text in seen_paths:
            fail(f"backup manifest contains an unsafe or duplicate path: {relative_text}")
        seen_paths.add(relative_text)
        kind = entry.get("kind")
        if kind not in ("sqlite", "jsonl"):
            fail(f"backup manifest contains an unsupported file kind: {kind!r}")
        allowed_sqlite = relative_text in ("state_5.sqlite", "sqlite/codex-dev.db")
        allowed_rollout = (
            len(relative.parts) >= 2
            and relative.parts[0] in ("sessions", "archived_sessions")
            and relative.suffix == ".jsonl"
        )
        if not (
            (kind == "sqlite" and allowed_sqlite)
            or (kind == "jsonl" and allowed_rollout)
        ):
            fail(f"unsupported restore target: {relative_text}")
        backup_file = backup_dir.joinpath(*relative.parts)
        destination = home.joinpath(*relative.parts)
        if backup_file.is_symlink() or not backup_file.is_file():
            fail(f"backup file is missing or unsafe: {backup_file}")
        if destination.is_symlink() or not destination.is_file():
            fail(f"restore destination is missing or unsafe: {destination}")
        try:
            backup_file.resolve().relative_to(backup_dir)
        except ValueError:
            fail(f"backup file resolves outside the backup directory: {backup_file}")
        assert_owned_file_inside_home(home, destination)
        if backup_file.stat().st_uid != os.getuid():
            fail(f"backup file must be owned by the current user: {backup_file}")
        expected_hash = entry.get("backup_sha256")
        if not isinstance(expected_hash, str) or sha256_file(backup_file) != expected_hash:
            fail(f"backup checksum mismatch: {backup_file}")
        source_mode = entry.get("source_mode")
        if not isinstance(source_mode, int) or source_mode < 0 or source_mode > 0o777:
            fail(f"backup manifest contains an invalid mode for {relative_text}")
        if kind == "sqlite":
            connection = sqlite3.connect(f"file:{backup_file}?mode=ro", uri=True)
            try:
                result = connection.execute("PRAGMA integrity_check").fetchone()
                if not result or result[0] != "ok":
                    fail(f"SQLite backup integrity check failed: {backup_file}")
            finally:
                connection.close()
        else:
            validate_jsonl(backup_file)
        resolved_entries.append((backup_file, destination, entry))
    return backup_dir, manifest, resolved_entries


def restore_backup(
    home: pathlib.Path,
    entries: list[tuple[pathlib.Path, pathlib.Path, dict[str, object]]],
) -> pathlib.Path:
    destinations = [destination for _, destination, _ in entries]
    assert_no_open_files(home, destinations)
    for _, destination, entry in entries:
        if entry["kind"] == "sqlite":
            assert_sqlite_writable(destination)
    ensure_disk_space(home, destinations)
    safety_backup = make_backup(home, destinations, "pre-restore", "")
    safety_manifest = json.loads(
        (safety_backup / "manifest.json").read_text(encoding="utf-8")
    )
    safety_entries = {entry["path"]: entry for entry in safety_manifest["files"]}

    for _, destination, _ in entries:
        relative = destination.relative_to(home).as_posix()
        if sha256_file(destination) != safety_entries[relative]["source_sha256"]:
            fail(f"restore destination changed after safety backup: {destination}")
    assert_no_open_files(home, destinations)

    replaced: list[pathlib.Path] = []
    try:
        for backup_file, destination, entry in entries:
            replace_from_file(
                backup_file,
                destination,
                int(entry["source_mode"]),
                entry["kind"] == "sqlite",
            )
            replaced.append(destination)
        for backup_file, destination, entry in entries:
            if sha256_file(destination) != sha256_file(backup_file):
                fail(f"restored file checksum mismatch: {destination}")
            if entry["kind"] == "sqlite":
                connection = sqlite3.connect(f"file:{destination}?mode=ro", uri=True)
                try:
                    result = connection.execute("PRAGMA integrity_check").fetchone()
                    if not result or result[0] != "ok":
                        fail(f"restored SQLite integrity check failed: {destination}")
                finally:
                    connection.close()
            else:
                validate_jsonl(destination)
    except BaseException:
        for destination in reversed(replaced):
            relative = destination.relative_to(home)
            entry = safety_entries[relative.as_posix()]
            replace_from_file(
                safety_backup / relative,
                destination,
                int(entry["source_mode"]),
                entry["kind"] == "sqlite",
            )
        detail = LAST_ERROR_MESSAGE or "restore failed"
        try:
            append_operation_log(safety_backup, "failed", detail)
        except OSError:
            pass
        raise
    append_operation_log(safety_backup, "restore-safety-snapshot-verified")
    return safety_backup


home = pathlib.Path(sys.argv[1]).expanduser().resolve()
provider = sys.argv[2]
apply = sys.argv[3] == "true"
repair = sys.argv[4] == "true"
restore = sys.argv[5]

if not home.is_dir():
    fail(f"Codex home does not exist or is not a directory: {home}")

if restore:
    restore_source, _, restore_entries = load_restore_manifest(restore, home)
    print(f"mode={'apply' if apply else 'dry-run'}")
    print(f"codex_home={home}")
    print(f"restore_source={restore_source}")
    print(f"restore_files={len(restore_entries)}")
    if apply:
        safety_backup = restore_backup(home, restore_entries)
        print("status=restored")
        print(f"safety_backup_dir={safety_backup}")
    raise SystemExit(0)

state_path = home / "state_5.sqlite"
catalog_path = home / "sqlite" / "codex-dev.db"
archived_thread_ids = read_archived_thread_ids(state_path)
state_rows = count_sqlite_changes(
    state_path, "threads", provider, optional=False, archived_thread_ids=archived_thread_ids
)
catalog_rows = count_sqlite_changes(
    catalog_path,
    "local_thread_catalog",
    provider,
    optional=True,
    archived_thread_ids=archived_thread_ids,
)
(
    rollout_plans,
    session_meta_records,
    item_id_repairs,
    encrypted_content_items,
    unknown_item_ids,
) = inspect_rollouts(rollout_paths(home), provider, repair)

print(f"mode={'apply' if apply else 'dry-run'}")
print(f"codex_home={home}")
print(f"target_provider={provider}")
print(f"state_rows={state_rows}")
print(f"catalog_rows={catalog_rows}")
print(f"archived_threads_skipped={len(archived_thread_ids)}")
print(f"rollout_files={len(rollout_plans)}")
print(f"session_meta_records={session_meta_records}")
print(f"item_id_repairs={item_id_repairs}")
print(f"encrypted_content_items={encrypted_content_items}")
print(f"unknown_item_ids={unknown_item_ids}")
if encrypted_content_items:
    print("warning=encrypted_content_present_cross_provider_resume_not_guaranteed")
if unknown_item_ids:
    print("warning=unknown_item_ids_not_repaired")

if apply:
    database_plans = [(state_path, "threads", state_rows)]
    if sqlite_table_available(catalog_path, "local_thread_catalog"):
        database_plans.append((catalog_path, "local_thread_catalog", catalog_rows))
    backup_dir = apply_switch(
        home, provider, database_plans, rollout_plans, archived_thread_ids
    )
    if backup_dir is None:
        print("status=no-changes")
    else:
        print("status=applied")
        print(f"backup_dir={backup_dir}")
PY
