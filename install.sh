#!/bin/sh
set -e

REPO="${WECLAW_REPO:-TingRuDeng/weclaw}"
BINARY="weclaw"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
CLAUDE_ACP_PACKAGE="@agentclientprotocol/claude-agent-acp"
CLAUDE_ACP_VERSION="${CLAUDE_ACP_VERSION:-0.58.1}"

github_download() {
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "User-Agent: weclaw-installer" -H "Authorization: Bearer ${TOKEN}" -o "$2" "$1"
  else
    curl -fsSL -H "User-Agent: weclaw-installer" -o "$2" "$1"
  fi
}

github_latest_url() {
  if [ -n "$TOKEN" ]; then
    curl -fsSLI -o /dev/null -w "%{url_effective}" -H "User-Agent: weclaw-installer" -H "Authorization: Bearer ${TOKEN}" "$1"
  else
    curl -fsSLI -o /dev/null -w "%{url_effective}" -H "User-Agent: weclaw-installer" "$1"
  fi
}

# 从发布摘要中提取目标资产的唯一 SHA-256，拒绝缺失、重复或非法摘要。
release_sha256() {
  checksum_file=$1
  asset_name=$2
  matches=$(awk -v target="$asset_name" '
    $2 == target || $2 == "*" target { print $1 }
  ' "$checksum_file")
  match_count=$(printf '%s\n' "$matches" | awk 'NF { count++ } END { print count + 0 }')
  if [ "$match_count" -ne 1 ]; then
    echo "错误：checksums.txt 中未找到唯一的 SHA-256：${asset_name}" >&2
    return 1
  fi
  if ! printf '%s\n' "$matches" | grep -Eq '^[[:xdigit:]]{64}$'; then
    echo "错误：${asset_name} 的 SHA-256 格式无效" >&2
    return 1
  fi
  printf '%s\n' "$matches" | tr '[:upper:]' '[:lower:]'
}

# 使用系统现有工具计算摘要，避免因缺少校验能力而静默安装。
file_sha256() {
  target_file=$1
  if command -v shasum >/dev/null 2>&1; then
    checksum_output=$(shasum -a 256 "$target_file") || return 1
  elif command -v sha256sum >/dev/null 2>&1; then
    checksum_output=$(sha256sum "$target_file") || return 1
  else
    echo "错误：安装需要 shasum 或 sha256sum 才能校验 SHA-256" >&2
    return 1
  fi
  printf '%s\n' "$checksum_output" | awk 'NR == 1 { print tolower($1) }'
}

verify_release_asset() {
  asset_file=$1
  checksum_file=$2
  asset_name=$3
  expected_sha=$(release_sha256 "$checksum_file" "$asset_name") || return 1
  actual_sha=$(file_sha256 "$asset_file") || return 1
  if [ "$actual_sha" != "$expected_sha" ]; then
    echo "错误：${asset_name} 的 SHA-256 校验失败" >&2
    return 1
  fi
}

latest_version() {
  latest_url=$(github_latest_url "https://github.com/${REPO}/releases/latest")
  version=${latest_url##*/tag/}
  if [ -z "$version" ] || [ "$version" = "$latest_url" ]; then
    echo "Error: could not determine latest version from ${latest_url}" >&2
    exit 1
  fi
  echo "$version"
}

# 从 PATH 中解析真实可执行文件，并统一返回绝对路径。
resolve_executable() {
  executable_name=$1
  previous_ifs=$IFS
  IFS=:
  for executable_dir in $PATH; do
    [ -n "$executable_dir" ] || executable_dir=.
    if [ -f "$executable_dir/$executable_name" ] && [ -x "$executable_dir/$executable_name" ]; then
      IFS=$previous_ifs
      absolute_dir=$(CDPATH= cd -- "$executable_dir" 2>/dev/null && pwd) || return 1
      printf '%s/%s\n' "$absolute_dir" "$executable_name"
      return 0
    fi
  done
  IFS=$previous_ifs
  return 1
}

absolute_file_path() {
  file_path=$1
  file_dir=${file_path%/*}
  file_name=${file_path##*/}
  absolute_dir=$(CDPATH= cd -- "$file_dir" 2>/dev/null && pwd) || return 1
  printf '%s/%s\n' "$absolute_dir" "$file_name"
}

claude_acp_install_command() {
  printf 'npm install -g %s@%s' "$CLAUDE_ACP_PACKAGE" "$CLAUDE_ACP_VERSION"
}

validate_claude_acp_version() {
  case "$CLAUDE_ACP_VERSION" in
    *[!0-9A-Za-z._+-]*)
      echo "CLAUDE_ACP_VERSION 无效，WeClaw 二进制已保留。" >&2
      return 1
      ;;
  esac
}

shell_quote() {
  escaped_value=$(printf '%s' "$1" | sed "s/'/'\\\\''/g")
  printf "'%s'" "$escaped_value"
}

configure_claude_agent() {
  installed_weclaw=$1
  claude_path=$2
  adapter_path=$3
  if "$installed_weclaw" config agent --name claude \
    --command "$adapter_path" --local-command "$claude_path"; then
    return 0
  fi
  echo "Claude ACP 配置失败，WeClaw 二进制已保留。" >&2
  echo "请运行以下命令修复：" >&2
  quoted_weclaw=$(shell_quote "$installed_weclaw")
  quoted_adapter=$(shell_quote "$adapter_path")
  quoted_claude=$(shell_quote "$claude_path")
  printf '  %s config agent --name claude --command %s --local-command %s\n' \
    "$quoted_weclaw" "$quoted_adapter" "$quoted_claude" >&2
  return 1
}

# Claude 存在时补齐 ACP adapter；显式跳过时不修改 npm 或配置。
setup_claude_acp() {
  installed_weclaw=$1
  [ "${WECLAW_SKIP_CLAUDE_ACP:-0}" != "1" ] || return 0
  claude_path=$(resolve_executable claude) || return 0
  adapter_path=$(resolve_executable claude-agent-acp) || adapter_path=
  if [ -z "$adapter_path" ]; then
    validate_claude_acp_version || return 1
    echo "正在安装 Claude ACP adapter ${CLAUDE_ACP_VERSION}..."
    if ! npm install -g "${CLAUDE_ACP_PACKAGE}@${CLAUDE_ACP_VERSION}"; then
      echo "Claude ACP 安装失败，WeClaw 二进制已保留。" >&2
      echo "请修复 npm 后运行：" >&2
      echo "  $(claude_acp_install_command)" >&2
      return 1
    fi
    adapter_path=$(resolve_executable claude-agent-acp) || {
      echo "Claude ACP 安装后未出现在 PATH，WeClaw 二进制已保留。" >&2
      echo "请运行以下命令修复：" >&2
      echo "  $(claude_acp_install_command)" >&2
      return 1
    }
  fi
  configure_claude_agent "$installed_weclaw" "$claude_path" "$adapter_path"
}

# Detect OS
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

# Detect architecture
ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

echo "Detected: ${OS}/${ARCH}"

# Download
FILENAME="${BINARY}_${OS}_${ARCH}"
VERSION="${WECLAW_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  VERSION=$(latest_version)
fi
URL="https://github.com/${REPO}/releases/download/${VERSION}/${FILENAME}"
CHECKSUM_URL="${URL%/*}/checksums.txt"

echo "Downloading ${URL}..."
TMP=$(mktemp)
CHECKSUM_TMP=$(mktemp)
cleanup_downloads() {
  rm -f "$TMP" "$CHECKSUM_TMP"
}
trap cleanup_downloads 0
trap 'exit 1' 1 2 15
github_download "$URL" "$TMP"
github_download "$CHECKSUM_URL" "$CHECKSUM_TMP"
verify_release_asset "$TMP" "$CHECKSUM_TMP" "$FILENAME"

# Install
chmod +x "$TMP"
if [ -d "$INSTALL_DIR" ] && [ -w "$INSTALL_DIR" ]; then
  mv "$TMP" "${INSTALL_DIR}/${BINARY}"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo mkdir -p "$INSTALL_DIR"
  sudo mv "$TMP" "${INSTALL_DIR}/${BINARY}"
fi
cleanup_downloads
trap - 0 1 2 15

# Clear macOS quarantine attributes
if [ "$OS" = "darwin" ]; then
  xattr -d com.apple.quarantine "${INSTALL_DIR}/${BINARY}" 2>/dev/null || true
  xattr -d com.apple.provenance "${INSTALL_DIR}/${BINARY}" 2>/dev/null || true
fi

INSTALLED_WECLAW=$(absolute_file_path "${INSTALL_DIR}/${BINARY}")
setup_claude_acp "$INSTALLED_WECLAW"

echo ""
echo "weclaw ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
echo ""
echo "Get started:"
echo "  weclaw start"
