#!/bin/sh
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
SYSTEM_PATH=/usr/bin:/bin:/usr/sbin:/sbin
PASS_COUNT=0

fail() {
  printf '失败：%s\n' "$*" >&2
  exit 1
}

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *) fail "输出缺少：$2\n实际输出：\n$1" ;;
  esac
}

assert_empty_file() {
  [ ! -s "$1" ] || fail "文件应为空：$1\n$(cat "$1")"
}

assert_file_contains() {
  grep -F "$2" "$1" >/dev/null || fail "文件 $1 缺少：$2"
}

# 为每个用例构造完全隔离的命令目录和安装目录。
setup_case() {
  CASE_DIR=$(mktemp -d "${TMPDIR:-/tmp}/weclaw-install-test.XXXXXX")
  CASE_DIR=$(CDPATH= cd -- "$CASE_DIR" && pwd)
  FAKE_BIN="$CASE_DIR/bin"
  INSTALL_DIR="$CASE_DIR/install"
  CALLS_FILE="$CASE_DIR/calls"
  DOWNLOADS_FILE="$CASE_DIR/downloads"
  mkdir -p "$FAKE_BIN" "$INSTALL_DIR"
  : >"$CALLS_FILE"
  : >"$DOWNLOADS_FILE"
  export CASE_DIR FAKE_BIN INSTALL_DIR CALLS_FILE DOWNLOADS_FILE
  create_base_commands
}

# 下载命令写入可执行的假 WeClaw，便于验证安装后配置调用。
create_base_commands() {
  cat >"$FAKE_BIN/uname" <<'EOF'
#!/bin/sh
[ "${1:-}" = "-m" ] && printf '%s\n' "${FAKE_UNAME_ARCH:-arm64}" || printf '%s\n' "${FAKE_UNAME_OS:-Darwin}"
EOF
  cat >"$FAKE_BIN/curl" <<'EOF'
#!/bin/sh
output=''
previous=''
url=''
for argument do
  [ "$previous" = "-o" ] && output=$argument
  previous=$argument
  url=$argument
done
if [ "$output" = "/dev/null" ]; then
  printf 'https://github.com/test/weclaw/releases/tag/v1.2.3'
  exit 0
fi
printf '%s\n' "$url" >>"$DOWNLOADS_FILE"
if [ "${url##*/}" = "checksums.txt" ]; then
  if [ "${FAKE_CHECKSUM_MISSING_ENTRY:-0}" = "1" ]; then
    printf '%s  %s\n' "${FAKE_EXPECTED_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" "unrelated_asset"
  else
    printf '%s  %s\n' "${FAKE_EXPECTED_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" "${FAKE_ASSET_NAME:-weclaw_darwin_arm64}"
  fi >"$output"
  exit 0
fi
cat >"$output" <<'SCRIPT'
#!/bin/sh
printf 'weclaw %s\n' "$*" >>"$CALLS_FILE"
[ "${FAKE_WECLAW_CONFIG_FAIL:-0}" = "1" ] && exit 23
exit 0
SCRIPT
EOF
  cat >"$FAKE_BIN/shasum" <<'EOF'
#!/bin/sh
[ "${1:-}" = "-a" ] && [ "${2:-}" = "256" ] || exit 64
shift 2
for file do
  printf '%s  %s\n' "${FAKE_ACTUAL_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" "$file"
done
EOF
  cat >"$FAKE_BIN/xattr" <<'EOF'
#!/bin/sh
exit 0
EOF
  cat >"$FAKE_BIN/sudo" <<'EOF'
#!/bin/sh
printf 'sudo %s\n' "$*" >>"$CALLS_FILE"
exit 97
EOF
  chmod +x "$FAKE_BIN/uname" "$FAKE_BIN/curl" "$FAKE_BIN/shasum" "$FAKE_BIN/xattr" "$FAKE_BIN/sudo"
}

add_claude() {
  cat >"$FAKE_BIN/claude" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod +x "$FAKE_BIN/claude"
}

add_adapter() {
  cat >"$FAKE_BIN/claude-agent-acp" <<'EOF'
#!/bin/sh
exit 0
EOF
  chmod +x "$FAKE_BIN/claude-agent-acp"
}

# 假 npm 记录参数，并按用例要求创建 adapter 或返回失败。
add_npm() {
  cat >"$FAKE_BIN/npm" <<'EOF'
#!/bin/sh
printf 'npm %s\n' "$*" >>"$CALLS_FILE"
[ "${FAKE_NPM_FAIL:-0}" = "1" ] && exit 19
cat >"$FAKE_BIN/claude-agent-acp" <<'ADAPTER'
#!/bin/sh
exit 0
ADAPTER
chmod +x "$FAKE_BIN/claude-agent-acp"
EOF
  chmod +x "$FAKE_BIN/npm"
}

run_installer() {
  output_file="$CASE_DIR/output"
  set +e
  PATH="$FAKE_BIN:$SYSTEM_PATH" WECLAW_REPO=test/weclaw INSTALL_DIR="$INSTALL_DIR" \
    sh "$ROOT_DIR/install.sh" >"$output_file" 2>&1
  status=$?
  set -e
  output=$(cat "$output_file")
}

finish_case() {
  name=$1
  rm -rf "$CASE_DIR"
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '通过：%s\n' "$name"
}

test_without_claude() {
  setup_case
  run_installer
  [ "$status" -eq 0 ] || fail "无 Claude 时安装失败：$output"
  assert_empty_file "$CALLS_FILE"
  finish_case "无 Claude 时不处理 ACP"
}

test_skip_claude_acp() {
  setup_case
  add_claude
  add_npm
  WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "跳过 ACP 时安装失败：$output"
  assert_empty_file "$CALLS_FILE"
  finish_case "显式跳过 ACP"
}

test_existing_adapter() {
  setup_case
  add_claude
  add_adapter
  run_installer
  [ "$status" -eq 0 ] || fail "已有 adapter 时安装失败：$output"
  assert_file_contains "$CALLS_FILE" "weclaw config agent --name claude --command $FAKE_BIN/claude-agent-acp --local-command $FAKE_BIN/claude"
  ! grep -F 'npm ' "$CALLS_FILE" >/dev/null || fail "已有 adapter 不应升级"
  finish_case "已有 adapter 仅配置"
}

test_install_default_version() {
  setup_case
  add_claude
  add_npm
  run_installer
  [ "$status" -eq 0 ] || fail "默认版本安装失败：$output"
  assert_file_contains "$CALLS_FILE" "npm install -g @agentclientprotocol/claude-agent-acp@0.58.1"
  assert_file_contains "$CALLS_FILE" "weclaw config agent --name claude --command $FAKE_BIN/claude-agent-acp --local-command $FAKE_BIN/claude"
  ! grep -F 'sudo npm' "$CALLS_FILE" >/dev/null || fail "禁止 sudo npm"
  finish_case "安装默认 ACP 版本"
}

test_install_overridden_version() {
  setup_case
  add_claude
  add_npm
  CLAUDE_ACP_VERSION=0.60.0 run_installer
  [ "$status" -eq 0 ] || fail "覆盖版本安装失败：$output"
  assert_file_contains "$CALLS_FILE" "npm install -g @agentclientprotocol/claude-agent-acp@0.60.0"
  finish_case "安装指定 ACP 版本"
}

test_rejects_invalid_version() {
  setup_case
  add_claude
  add_npm
  CLAUDE_ACP_VERSION='0.60.0 非法' run_installer
  [ "$status" -ne 0 ] || fail "非法版本应非零退出"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "非法版本时应保留 WeClaw"
  assert_contains "$output" "CLAUDE_ACP_VERSION 无效"
  assert_empty_file "$CALLS_FILE"
  finish_case "拒绝非法 ACP 版本"
}

test_install_failure_keeps_weclaw() {
  setup_case
  add_claude
  add_npm
  FAKE_NPM_FAIL=1 run_installer
  [ "$status" -ne 0 ] || fail "npm 失败时应非零退出"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "npm 失败时应保留 WeClaw"
  assert_contains "$output" "npm install -g @agentclientprotocol/claude-agent-acp@0.58.1"
  finish_case "安装失败保留 WeClaw 并给出修复命令"
}

test_missing_npm_keeps_weclaw() {
  setup_case
  add_claude
  run_installer
  [ "$status" -ne 0 ] || fail "缺少 npm 时应非零退出"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "缺少 npm 时应保留 WeClaw"
  assert_contains "$output" "npm install -g @agentclientprotocol/claude-agent-acp@0.58.1"
  finish_case "缺少 npm 时保留 WeClaw 并给出修复命令"
}

test_config_failure_keeps_weclaw() {
  setup_case
  INSTALL_DIR="$CASE_DIR/install target"
  mkdir -p "$INSTALL_DIR"
  export INSTALL_DIR
  add_claude
  add_adapter
  FAKE_WECLAW_CONFIG_FAIL=1 run_installer
  [ "$status" -ne 0 ] || fail "配置失败时应非零退出"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "配置失败时应保留 WeClaw"
  expected="'$INSTALL_DIR/weclaw' config agent --name claude --command '$FAKE_BIN/claude-agent-acp' --local-command '$FAKE_BIN/claude'"
  assert_contains "$output" "$expected"
  finish_case "配置失败保留 WeClaw 并给出修复命令"
}

test_checksum_success() {
  setup_case
  WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "摘要匹配时安装失败：$output"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "摘要匹配时应安装 WeClaw"
  assert_file_contains "$DOWNLOADS_FILE" "/v1.2.3/checksums.txt"
  finish_case "校验发布资产 SHA-256"
}
test_supported_release_targets() {
  for spec in \
    "Darwin arm64 weclaw_darwin_arm64" \
    "Darwin x86_64 weclaw_darwin_amd64" \
    "Linux aarch64 weclaw_linux_arm64" \
    "Linux x86_64 weclaw_linux_amd64"
  do
    set -- $spec
    setup_case
    FAKE_UNAME_OS=$1
    FAKE_UNAME_ARCH=$2
    FAKE_ASSET_NAME=$3
    export FAKE_UNAME_OS FAKE_UNAME_ARCH FAKE_ASSET_NAME
    WECLAW_SKIP_CLAUDE_ACP=1 run_installer
    [ "$status" -eq 0 ] || fail "$1/$2 安装失败：$output"
    assert_file_contains "$DOWNLOADS_FILE" "/v1.2.3/$3"
    finish_case "支持正式资产 $3"
    unset FAKE_UNAME_OS FAKE_UNAME_ARCH FAKE_ASSET_NAME
  done
}
test_checksum_mismatch_keeps_existing_binary() {
  setup_case
  printf 'existing binary\n' >"$INSTALL_DIR/weclaw"
  FAKE_EXPECTED_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
    WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -ne 0 ] || fail "摘要不匹配时应非零退出"
  assert_file_contains "$INSTALL_DIR/weclaw" "existing binary"
  assert_contains "$output" "SHA-256 校验失败"
  finish_case "摘要不匹配时不替换现有二进制"
}
test_checksum_missing_entry_keeps_existing_binary() {
  setup_case
  printf 'existing binary\n' >"$INSTALL_DIR/weclaw"
  FAKE_CHECKSUM_MISSING_ENTRY=1 WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -ne 0 ] || fail "摘要文件缺少资产条目时应非零退出"
  assert_file_contains "$INSTALL_DIR/weclaw" "existing binary"
  assert_contains "$output" "未找到唯一的 SHA-256"
  finish_case "摘要文件缺少资产条目时不替换现有二进制"
}
test_release_gate_runs_install_tests() {
  release_calls=$(/bin/bash -c '
    set -e
    WECLAW_RELEASE_SOURCE_ONLY=1 source "$1/scripts/release.sh"
    go() { :; }
    git() { :; }
    sh() { printf "sh %s\n" "$*"; }
    run_validations
  ' shell-test "$ROOT_DIR")
  assert_contains "$release_calls" "sh $ROOT_DIR/scripts/install_test.sh"
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '通过：发布门禁运行安装脚本测试\n'
}

test_without_claude
test_skip_claude_acp
test_existing_adapter
test_install_default_version
test_install_overridden_version
test_rejects_invalid_version
test_install_failure_keeps_weclaw
test_missing_npm_keeps_weclaw
test_config_failure_keeps_weclaw
test_checksum_success
test_supported_release_targets
test_checksum_mismatch_keeps_existing_binary
test_checksum_missing_entry_keeps_existing_binary
test_release_gate_runs_install_tests
printf '安装脚本测试全部通过：%s 个用例\n' "$PASS_COUNT"
