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

assert_file_not_contains() {
  ! grep -F "$2" "$1" >/dev/null || fail "文件 $1 不应包含：$2"
}

# 为每个用例构造完全隔离的命令目录和安装目录。
setup_case() {
  unset INSTALLER_INPUT_FILE WECLAW_INSTALL_INTERACTIVE WECLAW_SKIP_DEPENDENCY_SETUP
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
saw_https_proto=0
saw_tls12=0
for argument do
  [ "$previous" = "-o" ] && output=$argument
  [ "$previous" = "--proto" ] && [ "$argument" = "=https" ] && saw_https_proto=1
  [ "$argument" = "--tlsv1.2" ] && saw_tls12=1
  previous=$argument
  url=$argument
done
[ "$saw_https_proto" -eq 1 ] || exit 62
[ "$saw_tls12" -eq 1 ] || exit 63
printf '%s\n' "$url" >>"$DOWNLOADS_FILE"
case "$url" in
  https://github.com/*)
    [ -z "${FAKE_GITHUB_CURL_EXIT:-}" ] || exit "$FAKE_GITHUB_CURL_EXIT"
    [ "${FAKE_GITHUB_NETWORK_FAIL:-0}" != "1" ] || exit 7
    ;;
esac
if [ "$output" = "/dev/null" ]; then
  printf '%s\nhttps://github.com/test/weclaw/releases/tag/v1.2.3' "${FAKE_GITHUB_HTTP_CODE:-200}"
  exit 0
fi
case "$url" in
  https://gitee.com/api/v5/repos/*/releases/latest)
    printf '{"tag_name":"v1.2.3"}\n' >"$output"
    printf '200'
    exit 0
    ;;
esac
if [ "${url##*/}" = "checksums.txt" ]; then
  if [ "${FAKE_CHECKSUM_MISSING_ENTRY:-0}" = "1" ]; then
    printf '%s  %s\n' "${FAKE_EXPECTED_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" "unrelated_asset"
  else
    printf '%s  %s\n' "${FAKE_EXPECTED_SHA:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" "${FAKE_ASSET_NAME:-weclaw_darwin_arm64}"
  fi >"$output"
  printf '%s' "${FAKE_GITHUB_HTTP_CODE:-200}"
  exit 0
fi
cat >"$output" <<'SCRIPT'
#!/bin/sh
printf 'weclaw %s\n' "$*" >>"$CALLS_FILE"
if [ "$*" = "doctor --help" ]; then
  [ "${FAKE_WECLAW_NO_FIX:-0}" = "1" ] || printf '  --fix\n'
  exit 0
fi
[ "$*" != "doctor --fix" ] || {
  IFS= read -r dependency_choice || true
  printf 'doctor input %s\n' "$dependency_choice" >>"$CALLS_FILE"
}
[ "${FAKE_WECLAW_DOCTOR_FAIL:-0}" != "1" ] || exit 23
exit 0
SCRIPT
case "$url" in
  *.gz)
    mv "$output" "$output.raw"
    /usr/bin/gzip -n -9 -c "$output.raw" >"$output"
    rm -f "$output.raw"
    ;;
esac
printf '%s' "${FAKE_GITHUB_HTTP_CODE:-200}"
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
  if [ -n "${INSTALLER_INPUT_FILE:-}" ]; then
    PATH="$FAKE_BIN:$SYSTEM_PATH" WECLAW_REPO=test/weclaw \
      WECLAW_GITHUB_REPO=test/weclaw WECLAW_GITEE_REPO=test/weclaw \
      WECLAW_SOURCE="${WECLAW_SOURCE:-auto}" INSTALL_DIR="$INSTALL_DIR" \
      WECLAW_INSTALL_INTERACTIVE="${WECLAW_INSTALL_INTERACTIVE:-0}" \
      WECLAW_SKIP_DEPENDENCY_SETUP="${WECLAW_SKIP_DEPENDENCY_SETUP:-0}" \
      sh "$ROOT_DIR/install.sh" <"$INSTALLER_INPUT_FILE" >"$output_file" 2>&1
  else
    PATH="$FAKE_BIN:$SYSTEM_PATH" WECLAW_REPO=test/weclaw \
      WECLAW_GITHUB_REPO=test/weclaw WECLAW_GITEE_REPO=test/weclaw \
      WECLAW_SOURCE="${WECLAW_SOURCE:-auto}" INSTALL_DIR="$INSTALL_DIR" \
      WECLAW_INSTALL_INTERACTIVE="${WECLAW_INSTALL_INTERACTIVE:-0}" \
      WECLAW_SKIP_DEPENDENCY_SETUP="${WECLAW_SKIP_DEPENDENCY_SETUP:-0}" \
      sh "$ROOT_DIR/install.sh" >"$output_file" 2>&1
  fi
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

test_noninteractive_install_runs_read_only_doctor() {
  setup_case
  run_installer
  [ "$status" -eq 0 ] || fail "无 Claude 时安装失败：$output"
  assert_file_contains "$CALLS_FILE" "weclaw doctor"
  assert_file_not_contains "$CALLS_FILE" "weclaw doctor --fix"
  assert_contains "$output" "未检测到交互终端"
  assert_contains "$output" "weclaw doctor --fix"
  finish_case "非交互安装只检查并提示修复入口"
}

test_interactive_install_enters_dependency_wizard() {
  setup_case
  INSTALLER_INPUT_FILE="$CASE_DIR/input"
  printf '6\nn\n' >"$INSTALLER_INPUT_FILE"
  WECLAW_INSTALL_INTERACTIVE=1 run_installer
  [ "$status" -eq 0 ] || fail "安装失败：$output"
  assert_file_contains "$CALLS_FILE" "weclaw doctor --fix"
  assert_file_contains "$CALLS_FILE" "doctor input 6"
  finish_case "交互安装进入依赖选择向导"
}

test_existing_claude_is_not_modified_without_selection() {
  setup_case
  add_claude
  add_npm
  run_installer
  [ "$status" -eq 0 ] || fail "非交互安装失败：$output"
  assert_file_not_contains "$CALLS_FILE" "npm "
  assert_file_not_contains "$CALLS_FILE" "weclaw config agent"
  finish_case "已有 Claude 也不再未经选择安装 ACP"
}

test_dependency_wizard_failure_keeps_weclaw() {
  setup_case
  INSTALLER_INPUT_FILE="$CASE_DIR/input"
  printf '6\ny\n' >"$INSTALLER_INPUT_FILE"
  FAKE_WECLAW_DOCTOR_FAIL=1 WECLAW_INSTALL_INTERACTIVE=1 run_installer
  [ "$status" -ne 0 ] || fail "依赖向导失败应返回非零：$output"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "配置失败时应保留 WeClaw"
  finish_case "依赖向导失败保留 WeClaw"
}

test_explicit_skip_avoids_dependency_checks() {
  setup_case
  WECLAW_SKIP_DEPENDENCY_SETUP=1 run_installer
  [ "$status" -eq 0 ] || fail "显式跳过依赖向导失败：$output"
  assert_empty_file "$CALLS_FILE"
  finish_case "显式跳过依赖检查与安装"
}

test_noninteractive_hint_quotes_install_path() {
  setup_case
  INSTALL_DIR="$CASE_DIR/install target"
  mkdir -p "$INSTALL_DIR"
  export INSTALL_DIR
  run_installer
  [ "$status" -eq 0 ] || fail "空格路径安装失败：$output"
  assert_contains "$output" "'$INSTALL_DIR/weclaw' doctor --fix"
  finish_case "非交互修复命令安全引用安装路径"
}

test_old_release_without_fix_only_prints_upgrade_hint() {
  setup_case
  FAKE_WECLAW_NO_FIX=1 run_installer
  [ "$status" -eq 0 ] || fail "旧版兼容检查失败：$output"
  assert_file_not_contains "$CALLS_FILE" "weclaw doctor --fix"
  assert_contains "$output" "当前安装版本尚不支持依赖选择向导"
  finish_case "旧版二进制不调用未知 doctor fix 参数"
}

test_checksum_success() {
  setup_case
  WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "摘要匹配时安装失败：$output"
  [ -x "$INSTALL_DIR/weclaw" ] || fail "摘要匹配时应安装 WeClaw"
  assert_file_contains "$DOWNLOADS_FILE" "/v1.2.3/checksums.txt"
  finish_case "校验发布资产 SHA-256"
}
test_explicit_gitee_source_is_isolated() {
  setup_case
  WECLAW_SOURCE=gitee WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "显式 Gitee 安装失败：$output"
  assert_file_contains "$DOWNLOADS_FILE" "https://gitee.com/api/v5/repos/test/weclaw/releases/latest"
  assert_file_contains "$DOWNLOADS_FILE" "https://gitee.com/test/weclaw/releases/download/v1.2.3/weclaw_darwin_arm64.gz"
  assert_file_not_contains "$DOWNLOADS_FILE" "github.com"
  assert_file_contains "$INSTALL_DIR/weclaw" "#!/bin/sh"
  finish_case "显式 Gitee 来源不访问 GitHub"
}
test_auto_falls_back_on_github_network_failure() {
  setup_case
  FAKE_GITHUB_NETWORK_FAIL=1 WECLAW_SOURCE=auto WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "GitHub 网络失败后未切换 Gitee：$output"
  assert_file_contains "$DOWNLOADS_FILE" "github.com/test/weclaw/releases/latest"
  assert_file_contains "$DOWNLOADS_FILE" "gitee.com/api/v5/repos/test/weclaw/releases/latest"
  assert_contains "$output" "切换到 Gitee"
  finish_case "auto 仅在 GitHub 网络失败时切换 Gitee"
}
test_auto_asset_falls_back_on_network_failure() {
  setup_case
  FAKE_GITHUB_NETWORK_FAIL=1 WECLAW_VERSION=v1.2.3 WECLAW_SOURCE=auto WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -eq 0 ] || fail "GitHub 资产网络失败后未切换 Gitee：$output"
  assert_file_contains "$DOWNLOADS_FILE" "github.com/test/weclaw/releases/download/v1.2.3/weclaw_darwin_arm64"
  assert_file_contains "$DOWNLOADS_FILE" "gitee.com/test/weclaw/releases/download/v1.2.3/weclaw_darwin_arm64.gz"
  assert_contains "$output" "发布资产不可用"
  finish_case "auto 在资产网络失败时整组切换 Gitee"
}
test_auto_does_not_fallback_on_http_404() {
  setup_case
  FAKE_GITHUB_HTTP_CODE=404 WECLAW_VERSION=v1.2.3 WECLAW_SOURCE=auto WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -ne 0 ] || fail "GitHub 404 应失败关闭"
  assert_file_not_contains "$DOWNLOADS_FILE" "gitee.com"
  assert_contains "$output" "HTTP 404"
  finish_case "auto 不对 4xx 换源"
}
test_auto_does_not_fallback_on_truncated_download() {
  setup_case
  FAKE_GITHUB_CURL_EXIT=18 WECLAW_VERSION=v1.2.3 WECLAW_SOURCE=auto WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -ne 0 ] || fail "截断下载应失败关闭"
  assert_file_not_contains "$DOWNLOADS_FILE" "gitee.com"
  assert_contains "$output" "截断"
  finish_case "auto 不对截断下载换源"
}
test_rejects_unknown_source() {
  setup_case
  WECLAW_SOURCE=proxy WECLAW_SKIP_CLAUDE_ACP=1 run_installer
  [ "$status" -ne 0 ] || fail "未知来源应非零退出"
  assert_contains "$output" "WECLAW_SOURCE"
  assert_empty_file "$DOWNLOADS_FILE"
  finish_case "拒绝未知安装来源"
}
test_supported_release_targets() {
  for spec in \
    "Darwin arm64 weclaw_darwin_arm64" \
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
test_rejects_unpublished_release_targets() {
  for spec in "Darwin x86_64"; do
    set -- $spec
    setup_case
    FAKE_UNAME_OS=$1
    FAKE_UNAME_ARCH=$2
    export FAKE_UNAME_OS FAKE_UNAME_ARCH
    WECLAW_SKIP_CLAUDE_ACP=1 run_installer
    [ "$status" -ne 0 ] || fail "$1/$2 未发布平台应拒绝安装"
    assert_contains "$output" "Unsupported release platform"
    assert_empty_file "$DOWNLOADS_FILE"
    finish_case "拒绝未发布平台 $1/$2"
    unset FAKE_UNAME_OS FAKE_UNAME_ARCH
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
  assert_file_not_contains "$DOWNLOADS_FILE" "gitee.com"
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
    go() {
      if [ "${1:-}" = "list" ]; then
        printf "%s\n" "github.com/fastclaw-ai/weclaw"
      fi
    }
    git() { :; }
    sh() { printf "sh %s\n" "$*"; }
    run_validations
  ' shell-test "$ROOT_DIR")
  assert_contains "$release_calls" "sh $ROOT_DIR/scripts/install_test.sh"
  PASS_COUNT=$((PASS_COUNT + 1))
  printf '通过：发布门禁运行安装脚本测试\n'
}

test_noninteractive_install_runs_read_only_doctor
test_interactive_install_enters_dependency_wizard
test_existing_claude_is_not_modified_without_selection
test_dependency_wizard_failure_keeps_weclaw
test_explicit_skip_avoids_dependency_checks
test_noninteractive_hint_quotes_install_path
test_old_release_without_fix_only_prints_upgrade_hint
test_checksum_success
test_explicit_gitee_source_is_isolated
test_auto_falls_back_on_github_network_failure
test_auto_asset_falls_back_on_network_failure
test_auto_does_not_fallback_on_http_404
test_auto_does_not_fallback_on_truncated_download
test_rejects_unknown_source
test_supported_release_targets
test_rejects_unpublished_release_targets
test_checksum_mismatch_keeps_existing_binary
test_checksum_missing_entry_keeps_existing_binary
test_release_gate_runs_install_tests
printf '安装脚本测试全部通过：%s 个用例\n' "$PASS_COUNT"
