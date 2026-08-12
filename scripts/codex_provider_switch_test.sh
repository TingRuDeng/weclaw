#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
switch_script="${script_dir}/codex-provider-switch.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/codex-provider-switch-test.XXXXXX")"

cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  if [[ "${haystack}" != *"${needle}"* ]]; then
    fail "expected output to contain: ${needle}"
  fi
}

fixture_snapshot() {
  python3 - "$1" <<'PY'
import hashlib
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
for path in sorted(candidate for candidate in root.rglob("*") if candidate.is_file()):
    relative = path.relative_to(root).as_posix()
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    print(f"{relative} {digest}")
PY
}

create_fixture() {
  local codex_home="$1"
  mkdir -p "${codex_home}/sessions/2026/08/05" "${codex_home}/archived_sessions" "${codex_home}/sqlite"
  python3 - "${codex_home}" <<'PY'
import json
import pathlib
import sqlite3
import sys

root = pathlib.Path(sys.argv[1])

state = sqlite3.connect(root / "state_5.sqlite")
state.execute("CREATE TABLE threads (id TEXT PRIMARY KEY, model_provider TEXT NOT NULL)")
state.executemany(
    "INSERT INTO threads (id, model_provider) VALUES (?, ?)",
    [("thread-current", "OpenAI"), ("thread-archived", "OpenAI")],
)
state.commit()
state.close()

catalog = sqlite3.connect(root / "sqlite" / "codex-dev.db")
catalog.execute(
    "CREATE TABLE local_thread_catalog (thread_id TEXT PRIMARY KEY, model_provider TEXT NOT NULL)"
)
catalog.executemany(
    "INSERT INTO local_thread_catalog (thread_id, model_provider) VALUES (?, ?)",
    [("thread-current", "OpenAI"), ("thread-archived", "OpenAI")],
)
catalog.commit()
catalog.close()

current_records = [
    {
        "timestamp": "2026-08-05T00:00:00Z",
        "type": "session_meta",
        "payload": {"id": "thread-current", "model_provider": "OpenAI"},
    },
    {
        "timestamp": "2026-08-05T00:00:01Z",
        "type": "response_item",
        "payload": {
            "type": "function_call",
            "id": "item_function",
            "call_id": "call_function",
            "name": "shell",
            "arguments": "{}",
        },
    },
    {
        "timestamp": "2026-08-05T00:00:02Z",
        "type": "response_item_reference",
        "payload": {"item_id": "item_function"},
    },
    {
        "timestamp": "2026-08-05T00:00:03Z",
        "type": "response_item",
        "payload": {
            "type": "reasoning",
            "id": "item_reasoning",
            "encrypted_content": "encrypted-fixture",
            "summary": [],
        },
    },
    {
        "timestamp": "2026-08-05T00:00:04Z",
        "type": "session_meta",
        "payload": {"id": "thread-current", "model_provider": "OpenAI"},
    },
]
archived_records = [
    {
        "timestamp": "2026-08-04T00:00:00Z",
        "type": "session_meta",
        "payload": {"id": "thread-archived", "model_provider": "OpenAI"},
    },
    {
        "timestamp": "2026-08-04T00:00:01Z",
        "type": "response_item",
        "payload": {
            "type": "custom_tool_call",
            "id": "item_custom",
            "call_id": "call_custom",
            "name": "custom",
            "input": "fixture",
        },
    },
]

for path, records in (
    (root / "sessions" / "2026" / "08" / "05" / "current.jsonl", current_records),
    (root / "archived_sessions" / "archived.jsonl", archived_records),
):
    path.write_text(
        "".join(json.dumps(record, separators=(",", ":")) + "\n" for record in records),
        encoding="utf-8",
    )
PY
}

test_dry_run_reports_changes_without_writing() {
  local codex_home="${test_root}/dry-run-home"
  local before
  local after
  local output

  create_fixture "${codex_home}"
  before="$(fixture_snapshot "${codex_home}")"
  [[ -x "${switch_script}" ]] || fail "switch script is missing or not executable"

  output="$("${switch_script}" openai --repair-item-ids --codex-home "${codex_home}")"
  after="$(fixture_snapshot "${codex_home}")"

  [[ "${after}" == "${before}" ]] || fail "dry-run modified fixture files"
  assert_contains "${output}" "mode=dry-run"
  assert_contains "${output}" "target_provider=openai"
  assert_contains "${output}" "state_rows=2"
  assert_contains "${output}" "catalog_rows=2"
  assert_contains "${output}" "rollout_files=2"
  assert_contains "${output}" "session_meta_records=3"
  assert_contains "${output}" "item_id_repairs=3"
  assert_contains "${output}" "encrypted_content_items=1"
}

test_apply_updates_all_stores_and_repairs_ids() {
  local codex_home="${test_root}/apply-home"
  local output
  local backup_dir

  create_fixture "${codex_home}"
  output="$("${switch_script}" openai --repair-item-ids --apply --codex-home "${codex_home}")"
  assert_contains "${output}" "mode=apply"
  assert_contains "${output}" "status=applied"
  backup_dir="$(printf '%s\n' "${output}" | awk -F= '$1 == "backup_dir" {print substr($0, index($0, "=") + 1)}')"
  [[ -n "${backup_dir}" && -d "${backup_dir}" ]] || fail "apply did not create a backup directory"
  [[ -f "${backup_dir}/manifest.json" ]] || fail "backup manifest is missing"
  [[ -f "${backup_dir}/operation.log" ]] || fail "backup operation log is missing"
  assert_contains "$(<"${backup_dir}/operation.log")" "status=backup-verified"
  assert_contains "$(<"${backup_dir}/operation.log")" "status=applied-and-verified"

  python3 - "${codex_home}" "${backup_dir}" <<'PY'
import json
import pathlib
import sqlite3
import sys

home = pathlib.Path(sys.argv[1])
backup = pathlib.Path(sys.argv[2])

for database_path, table in (
    (home / "state_5.sqlite", "threads"),
    (home / "sqlite" / "codex-dev.db", "local_thread_catalog"),
):
    connection = sqlite3.connect(database_path)
    providers = {row[0] for row in connection.execute(f"SELECT model_provider FROM {table}")}
    integrity = connection.execute("PRAGMA integrity_check").fetchone()[0]
    connection.close()
    if providers != {"openai"}:
        raise SystemExit(f"unexpected providers in {database_path}: {providers}")
    if integrity != "ok":
        raise SystemExit(f"integrity check failed for {database_path}: {integrity}")

records = []
for path in sorted((home / "sessions").rglob("*.jsonl")) + sorted(
    (home / "archived_sessions").rglob("*.jsonl")
):
    records.extend(json.loads(line) for line in path.read_text(encoding="utf-8").splitlines())

providers = {
    record["payload"]["model_provider"]
    for record in records
    if record.get("type") == "session_meta"
}
if providers != {"openai"}:
    raise SystemExit(f"unexpected rollout providers: {providers}")

response_items = {
    record["payload"]["type"]: record["payload"]
    for record in records
    if record.get("type") == "response_item"
}
expected_ids = {
    "function_call": "fc_function",
    "reasoning": "rs_reasoning",
    "custom_tool_call": "ctc_custom",
}
actual_ids = {item_type: response_items[item_type]["id"] for item_type in expected_ids}
if actual_ids != expected_ids:
    raise SystemExit(f"unexpected repaired IDs: {actual_ids}")
if response_items["function_call"]["call_id"] != "call_function":
    raise SystemExit("function call_id was modified")
references = [
    record["payload"]["item_id"]
    for record in records
    if record.get("type") == "response_item_reference"
]
if references != ["fc_function"]:
    raise SystemExit(f"item ID references were not updated: {references}")

manifest = json.loads((backup / "manifest.json").read_text(encoding="utf-8"))
if (
    manifest.get("tool") != "codex-provider-switch"
    or manifest.get("version") != 1
    or manifest.get("backup_verified") is not True
):
    raise SystemExit("backup manifest identity is invalid")
backed_up_paths = {entry["path"] for entry in manifest.get("files", [])}
expected_paths = {
    "state_5.sqlite",
    "sqlite/codex-dev.db",
    "sessions/2026/08/05/current.jsonl",
    "archived_sessions/archived.jsonl",
}
if backed_up_paths != expected_paths:
    raise SystemExit(f"unexpected backup paths: {backed_up_paths}")
PY
}

test_bidirectional_idempotent_and_restore() {
  local codex_home="${test_root}/restore-home"
  local first_output
  local first_backup
  local backup_count_before
  local backup_count_after
  local no_change_output
  local reverse_output
  local restore_preview
  local restore_output

  create_fixture "${codex_home}"
  first_output="$("${switch_script}" openai --repair-item-ids --apply --codex-home "${codex_home}")"
  first_backup="$(printf '%s\n' "${first_output}" | awk -F= '$1 == "backup_dir" {print substr($0, index($0, "=") + 1)}')"
  [[ -d "${first_backup}" ]] || fail "first apply backup is missing"

  backup_count_before="$(find "${codex_home}/backups" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
  no_change_output="$("${switch_script}" openai --repair-item-ids --apply --codex-home "${codex_home}")"
  backup_count_after="$(find "${codex_home}/backups" -mindepth 1 -maxdepth 1 -type d | wc -l | tr -d ' ')"
  assert_contains "${no_change_output}" "status=no-changes"
  [[ "${backup_count_after}" == "${backup_count_before}" ]] || fail "idempotent apply created a backup"

  reverse_output="$("${switch_script}" OpenAI --apply --codex-home "${codex_home}")"
  assert_contains "${reverse_output}" "target_provider=OpenAI"
  assert_contains "${reverse_output}" "status=applied"

  restore_preview="$("${switch_script}" --restore "${first_backup}" --codex-home "${codex_home}")"
  assert_contains "${restore_preview}" "mode=dry-run"
  assert_contains "${restore_preview}" "restore_files=4"
  restore_output="$("${switch_script}" --restore "${first_backup}" --apply --codex-home "${codex_home}")"
  assert_contains "${restore_output}" "status=restored"
  assert_contains "${restore_output}" "restore_source=${first_backup}"
  assert_contains "${restore_output}" "safety_backup_dir="

  python3 - "${codex_home}" <<'PY'
import json
import pathlib
import sqlite3
import sys

home = pathlib.Path(sys.argv[1])
for database_path, table in (
    (home / "state_5.sqlite", "threads"),
    (home / "sqlite" / "codex-dev.db", "local_thread_catalog"),
):
    connection = sqlite3.connect(database_path)
    providers = {row[0] for row in connection.execute(f"SELECT model_provider FROM {table}")}
    connection.close()
    if providers != {"OpenAI"}:
        raise SystemExit(f"restore did not recover {database_path}: {providers}")

records = []
for path in sorted((home / "sessions").rglob("*.jsonl")) + sorted(
    (home / "archived_sessions").rglob("*.jsonl")
):
    records.extend(json.loads(line) for line in path.read_text(encoding="utf-8").splitlines())
providers = {
    record["payload"]["model_provider"]
    for record in records
    if record.get("type") == "session_meta"
}
if providers != {"OpenAI"}:
    raise SystemExit(f"restore did not recover rollout providers: {providers}")
item_ids = {
    record["payload"]["id"]
    for record in records
    if record.get("type") == "response_item"
}
if item_ids != {"item_function", "item_reasoning", "item_custom"}:
    raise SystemExit(f"restore did not recover original item IDs: {item_ids}")
PY
}

test_unknown_item_ids_are_reported_as_unrepaired() {
  local codex_home="${test_root}/unknown-item-home"
  local rollout
  local output

  create_fixture "${codex_home}"
  rollout="${codex_home}/sessions/2026/08/05/current.jsonl"
  python3 - "${rollout}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
record = {
    "timestamp": "2026-08-05T00:00:05Z",
    "type": "response_item",
    "payload": {"type": "relay_specific_item", "id": "item_unknown"},
}
with path.open("a", encoding="utf-8") as handle:
    handle.write(json.dumps(record, separators=(",", ":")) + "\n")
PY

  output="$("${switch_script}" openai --repair-item-ids --codex-home "${codex_home}")"
  assert_contains "${output}" "unknown_item_ids=1"
  assert_contains "${output}" "warning=unknown_item_ids_not_repaired"
}

test_apply_refuses_a_running_codex_process_for_the_target_home() {
  local codex_home="${test_root}/active-process-home"
  local helper_dir="${test_root}/active-process-helper"
  local stop_file="${test_root}/stop-active-process"
  local ready_file="${test_root}/active-process-ready"
  local helper_pid
  local output
  local status

  create_fixture "${codex_home}"
  mkdir -p "${helper_dir}"
  python3 - "${helper_dir}/codex" <<'PY'
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
path.write_text(
    "#!/usr/bin/env bash\n"
    "exec 9<>\"$2/state_5.sqlite\"\n"
    ": >\"$4\"\n"
    "while [[ ! -e \"$3\" ]]; do /bin/sleep 0.05; done\n",
    encoding="utf-8",
)
os.chmod(path, 0o755)
PY
  "${helper_dir}/codex" app-server "${codex_home}" "${stop_file}" "${ready_file}" &
  helper_pid=$!
  while [[ ! -e "${ready_file}" ]]; do
    /bin/sleep 0.01
  done
  set +e
  output="$("${switch_script}" openai --apply --codex-home "${codex_home}" 2>&1)"
  status=$?
  set -e
  : >"${stop_file}"
  wait "${helper_pid}"

  [[ ${status} -ne 0 ]] || fail "apply succeeded while a Codex process targeted the fixture home"
  assert_contains "${output}" "active Codex/WeClaw process"
  python3 - "${codex_home}/state_5.sqlite" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
providers = {row[0] for row in connection.execute("SELECT model_provider FROM threads")}
connection.close()
if providers != {"OpenAI"}:
    raise SystemExit(f"writer gate changed the database: {providers}")
PY
}

test_apply_ignores_open_files_outside_migration_targets() {
  local codex_home="${test_root}/unrelated-open-file-home"
  local helper_dir="${test_root}/unrelated-open-file-helper"
  local unrelated_file
  local apply_status

  create_fixture "${codex_home}"
  unrelated_file="${codex_home}/generated_images/unrelated.txt"
  mkdir -p "$(dirname "${unrelated_file}")"
  printf '%s\n' "read-only fixture" >"${unrelated_file}"
  mkdir -p "${helper_dir}"
  python3 - "${helper_dir}/lsof" <<'PY'
import os
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
path.write_text(
    "#!/usr/bin/env bash\n"
    "if [[ \" $* \" == *\" +D \"* ]]; then\n"
    "  echo 424242\n"
    "  exit 0\n"
    "fi\n"
    "exit 1\n",
    encoding="utf-8",
)
os.chmod(path, 0o755)
PY

  set +e
  PATH="${helper_dir}:${PATH}" "${switch_script}" openai --apply --codex-home "${codex_home}" >/dev/null
  apply_status=$?
  set -e

  [[ ${apply_status} -eq 0 ]] || fail "apply rejected an open file outside migration targets"
}

test_apply_does_not_delete_rollout_adjacent_files() {
  local codex_home="${test_root}/rollout-adjacent-home"
  local adjacent_file

  create_fixture "${codex_home}"
  adjacent_file="${codex_home}/sessions/2026/08/05/current.jsonl-wal"
  printf '%s\n' "unrelated-fixture" >"${adjacent_file}"
  "${switch_script}" openai --apply --codex-home "${codex_home}" >/dev/null
  [[ -f "${adjacent_file}" ]] || fail "apply deleted a rollout-adjacent file"
  [[ "$(<"${adjacent_file}")" == "unrelated-fixture" ]] || fail "apply changed a rollout-adjacent file"
}

test_valid_json_with_unicode_next_line_is_accepted() {
  local codex_home="${test_root}/unicode-next-line-home"
  local rollout
  local output

  create_fixture "${codex_home}"
  rollout="${codex_home}/sessions/2026/08/05/current.jsonl"
  python3 - "${rollout}" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
record = {
    "timestamp": "2026-08-05T00:00:05Z",
    "type": "event_msg",
    "payload": {"type": "agent_reasoning", "text": "before\u0085after"},
}
with path.open("a", encoding="utf-8") as handle:
    handle.write(json.dumps(record, ensure_ascii=False, separators=(",", ":")) + "\n")
PY

  output="$("${switch_script}" openai --repair-item-ids --codex-home "${codex_home}")"
  assert_contains "${output}" "mode=dry-run"
  assert_contains "${output}" "rollout_files=2"
}

test_invalid_json_and_id_collisions_fail_without_writing() {
  local malformed_home="${test_root}/malformed-home"
  local collision_home="${test_root}/collision-home"
  local before
  local after
  local output
  local status

  create_fixture "${malformed_home}"
  printf '%s\n' '{"broken":' >>"${malformed_home}/sessions/2026/08/05/current.jsonl"
  before="$(fixture_snapshot "${malformed_home}")"
  set +e
  output="$("${switch_script}" openai --apply --codex-home "${malformed_home}" 2>&1)"
  status=$?
  set -e
  after="$(fixture_snapshot "${malformed_home}")"
  [[ ${status} -ne 0 ]] || fail "apply accepted malformed rollout JSON"
  assert_contains "${output}" "invalid JSON"
  [[ "${after}" == "${before}" ]] || fail "malformed JSON failure changed fixture files"

  create_fixture "${collision_home}"
  python3 - "${collision_home}/sessions/2026/08/05/current.jsonl" <<'PY'
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
record = {
    "timestamp": "2026-08-05T00:00:05Z",
    "type": "response_item",
    "payload": {
        "type": "function_call",
        "id": "fc_function",
        "call_id": "call_collision",
        "name": "shell",
        "arguments": "{}",
    },
}
with path.open("a", encoding="utf-8") as handle:
    handle.write(json.dumps(record, separators=(",", ":")) + "\n")
PY
  before="$(fixture_snapshot "${collision_home}")"
  set +e
  output="$("${switch_script}" openai --repair-item-ids --apply --codex-home "${collision_home}" 2>&1)"
  status=$?
  set -e
  after="$(fixture_snapshot "${collision_home}")"
  [[ ${status} -ne 0 ]] || fail "apply accepted a colliding item ID repair"
  assert_contains "${output}" "item ID repair would collide"
  [[ "${after}" == "${before}" ]] || fail "collision failure changed fixture files"
}

test_restore_rejects_a_corrupted_backup() {
  local codex_home="${test_root}/corrupt-backup-home"
  local apply_output
  local backup_dir
  local before
  local after
  local output
  local status

  create_fixture "${codex_home}"
  apply_output="$("${switch_script}" openai --apply --codex-home "${codex_home}")"
  backup_dir="$(printf '%s\n' "${apply_output}" | awk -F= '$1 == "backup_dir" {print substr($0, index($0, "=") + 1)}')"
  [[ -d "${backup_dir}" ]] || fail "corruption test backup is missing"
  printf '%s\n' 'tampered' >>"${backup_dir}/archived_sessions/archived.jsonl"
  before="$(fixture_snapshot "${codex_home}")"

  set +e
  output="$("${switch_script}" --restore "${backup_dir}" --apply --codex-home "${codex_home}" 2>&1)"
  status=$?
  set -e
  after="$(fixture_snapshot "${codex_home}")"

  [[ ${status} -ne 0 ]] || fail "restore accepted a corrupted backup"
  assert_contains "${output}" "backup checksum mismatch"
  [[ "${after}" == "${before}" ]] || fail "corrupt restore attempt changed fixture files"
}

test_provider_name_is_case_exact() {
  local codex_home="${test_root}/invalid-provider-home"
  local output
  local status

  create_fixture "${codex_home}"
  set +e
  output="$("${switch_script}" OPENAI --codex-home "${codex_home}" 2>&1)"
  status=$?
  set -e
  [[ ${status} -eq 2 ]] || fail "invalid provider returned status ${status}, expected 2"
  assert_contains "${output}" "不支持的参数：OPENAI"
}

test_apply_rejects_a_symlinked_backup_root() {
  local codex_home="${test_root}/symlink-backup-home"
  local outside_backup="${test_root}/outside-backups"
  local output
  local status

  create_fixture "${codex_home}"
  mkdir -p "${outside_backup}"
  ln -s "${outside_backup}" "${codex_home}/backups"
  set +e
  output="$("${switch_script}" openai --apply --codex-home "${codex_home}" 2>&1)"
  status=$?
  set -e
  [[ ${status} -ne 0 ]] || fail "apply accepted a symlinked backup root"
  assert_contains "${output}" "backup root must be a real directory"
  if find "${outside_backup}" -mindepth 1 -print -quit | grep -q .; then
    fail "apply wrote through a symlinked backup root"
  fi
}

test_restore_rejects_manifest_targets_outside_codex_stores() {
  local codex_home="${test_root}/forged-restore-home"
  local backup_dir
  local output
  local status

  create_fixture "${codex_home}"
  backup_dir="${codex_home}/backups/forged"
  mkdir -p "${backup_dir}"
  python3 - "${codex_home}" "${backup_dir}" <<'PY'
import hashlib
import json
import os
import pathlib
import sys

home = pathlib.Path(sys.argv[1])
backup = pathlib.Path(sys.argv[2])
target = home / "unrelated.jsonl"
target.write_text('{"safe":true}\n', encoding="utf-8")
backup_file = backup / "unrelated.jsonl"
backup_file.write_text('{"safe":false}\n', encoding="utf-8")
digest = hashlib.sha256(backup_file.read_bytes()).hexdigest()
manifest = {
    "tool": "codex-provider-switch",
    "version": 1,
    "codex_home": str(home.resolve()),
    "files": [
        {
            "path": "unrelated.jsonl",
            "kind": "jsonl",
            "source_mode": 0o600,
            "backup_sha256": digest,
        }
    ],
}
(backup / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
os.chmod(target, 0o600)
PY

  set +e
  output="$("${switch_script}" --restore "${backup_dir}" --codex-home "${codex_home}" 2>&1)"
  status=$?
  set -e
  [[ ${status} -ne 0 ]] || fail "restore accepted an unrelated manifest target"
  assert_contains "${output}" "unsupported restore target"
}

test_apply_updates_only_sqlite_rows_that_need_switching() {
  local codex_home="${test_root}/targeted-sqlite-home"

  create_fixture "${codex_home}"
  python3 - "${codex_home}/state_5.sqlite" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
connection.execute("ALTER TABLE threads ADD COLUMN provider_update_count INTEGER NOT NULL DEFAULT 0")
connection.execute("UPDATE threads SET model_provider = 'openai' WHERE id = 'thread-current'")
connection.execute(
    """
    CREATE TRIGGER count_provider_updates
    AFTER UPDATE OF model_provider ON threads
    BEGIN
      UPDATE threads
      SET provider_update_count = provider_update_count + 1
      WHERE id = NEW.id;
    END
    """
)
connection.commit()
connection.close()
PY

  "${switch_script}" openai --apply --codex-home "${codex_home}" >/dev/null
  python3 - "${codex_home}/state_5.sqlite" <<'PY'
import sqlite3
import sys

connection = sqlite3.connect(sys.argv[1])
rows = dict(connection.execute("SELECT id, provider_update_count FROM threads"))
connection.close()
expected = {"thread-current": 0, "thread-archived": 1}
if rows != expected:
    raise SystemExit(f"provider update touched unchanged rows: {rows}")
PY
}

test_dry_run_reports_changes_without_writing
echo "PASS: dry-run reports planned changes without writing"
test_apply_updates_all_stores_and_repairs_ids
echo "PASS: apply updates all stores and repairs item IDs"
test_bidirectional_idempotent_and_restore
echo "PASS: bidirectional switching is idempotent and backups restore"
test_unknown_item_ids_are_reported_as_unrepaired
echo "PASS: unknown item IDs are reported without unsafe rewriting"
test_apply_refuses_a_running_codex_process_for_the_target_home
echo "PASS: apply refuses an active Codex process for the target home"
test_apply_ignores_open_files_outside_migration_targets
echo "PASS: apply ignores open files outside migration targets"
test_apply_does_not_delete_rollout_adjacent_files
echo "PASS: apply leaves rollout-adjacent files untouched"
test_valid_json_with_unicode_next_line_is_accepted
echo "PASS: valid JSON strings containing Unicode next line are accepted"
test_invalid_json_and_id_collisions_fail_without_writing
echo "PASS: malformed JSON and item ID collisions fail without writes"
test_restore_rejects_a_corrupted_backup
echo "PASS: restore rejects corrupted backups"
test_provider_name_is_case_exact
echo "PASS: provider names are case-exact"
test_apply_rejects_a_symlinked_backup_root
echo "PASS: apply rejects a symlinked backup root"
test_restore_rejects_manifest_targets_outside_codex_stores
echo "PASS: restore rejects targets outside the Codex stores"
test_apply_updates_only_sqlite_rows_that_need_switching
echo "PASS: apply updates only SQLite rows that need switching"
