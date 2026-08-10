#!/bin/sh
set -e

GITHUB_REPO="${WECLAW_GITHUB_REPO:-${WECLAW_REPO:-TingRuDeng/weclaw}}"
GITEE_REPO="${WECLAW_GITEE_REPO:-jimdeng891/weclaw}"
RELEASE_SOURCE=$(printf '%s' "${WECLAW_SOURCE:-auto}" | tr '[:upper:]' '[:lower:]')
BINARY="weclaw"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
TOKEN="${GITHUB_TOKEN:-${GH_TOKEN:-}}"

case "$RELEASE_SOURCE" in
  auto|github|gitee) ;;
  *)
    echo "错误：WECLAW_SOURCE 必须是 auto、github 或 gitee" >&2
    exit 1
    ;;
esac

classify_curl_failure() {
  curl_status=$1
  curl_context=$2
  case "$curl_status" in
    5|6|7|28|35|47|52|55|56|60|77|92)
      CURL_FAILURE_STATUS=75
      ;;
    18)
      echo "错误：${curl_context} 下载截断" >&2
      CURL_FAILURE_STATUS=1
      ;;
    *)
      echo "错误：${curl_context} 下载失败（curl ${curl_status}）" >&2
      CURL_FAILURE_STATUS=1
      ;;
  esac
}

# 返回 75 表示网络、TLS、超时或 5xx，可由 auto 策略换源；其他 HTTP 错误失败关闭。
release_download() {
  download_source=$1
  download_url=$2
  download_output=$3
  if [ "$download_source" = "github" ] && [ -n "$TOKEN" ]; then
    if http_code=$(curl -sSL --proto '=https' --tlsv1.2 -H "User-Agent: weclaw-installer" -H "Authorization: Bearer ${TOKEN}" -o "$download_output" -w '%{http_code}' "$download_url"); then
      :
    else
      classify_curl_failure "$?" "$download_source"
      return "$CURL_FAILURE_STATUS"
    fi
  else
    if http_code=$(curl -sSL --proto '=https' --tlsv1.2 -H "User-Agent: weclaw-installer" -o "$download_output" -w '%{http_code}' "$download_url"); then
      :
    else
      classify_curl_failure "$?" "$download_source"
      return "$CURL_FAILURE_STATUS"
    fi
  fi
  case "$http_code" in
    200)
      download_bytes=$(wc -c <"$download_output" | tr -d '[:space:]')
      if [ "$download_bytes" -gt 134217728 ]; then
        echo "错误：下载超过 128 MiB 限制" >&2
        return 1
      fi
      return 0
      ;;
    5??)
      echo "${download_source} 发布源服务端暂不可用（HTTP ${http_code}）" >&2
      return 75
      ;;
    *)
      echo "${download_source} 发布源返回 HTTP ${http_code}" >&2
      return 1
      ;;
  esac
}

github_latest_version() {
  if [ -n "$TOKEN" ]; then
    if latest_result=$(curl -sSLI --proto '=https' --tlsv1.2 -o /dev/null -w '%{http_code}\n%{url_effective}' -H "User-Agent: weclaw-installer" -H "Authorization: Bearer ${TOKEN}" "https://github.com/${GITHUB_REPO}/releases/latest"); then
      :
    else
      classify_curl_failure "$?" "GitHub latest"
      return "$CURL_FAILURE_STATUS"
    fi
  else
    if latest_result=$(curl -sSLI --proto '=https' --tlsv1.2 -o /dev/null -w '%{http_code}\n%{url_effective}' -H "User-Agent: weclaw-installer" "https://github.com/${GITHUB_REPO}/releases/latest"); then
      :
    else
      classify_curl_failure "$?" "GitHub latest"
      return "$CURL_FAILURE_STATUS"
    fi
  fi
  latest_code=$(printf '%s\n' "$latest_result" | sed -n '1p')
  latest_url=$(printf '%s\n' "$latest_result" | sed -n '2p')
  case "$latest_code" in
    200) ;;
    5??) return 75 ;;
    *) echo "GitHub latest 返回 HTTP ${latest_code}" >&2; return 1 ;;
  esac
  version=${latest_url##*/tag/}
  validate_release_version "$version" || return 1
  VERSION=$version
}

gitee_latest_version() {
  latest_file=$(mktemp)
  release_download gitee "https://gitee.com/api/v5/repos/${GITEE_REPO}/releases/latest" "$latest_file" || {
    latest_status=$?
    rm -f "$latest_file"
    return "$latest_status"
  }
  tag_matches=$(tr '{},' '\n' <"$latest_file" | sed -n 's/^[[:space:]]*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)"[[:space:]]*$/\1/p')
  rm -f "$latest_file"
  tag_count=$(printf '%s\n' "$tag_matches" | awk 'NF { count++ } END { print count + 0 }')
  if [ "$tag_count" -ne 1 ]; then
    echo "错误：无法从 Gitee latest 响应取得唯一 tag_name" >&2
    return 1
  fi
  validate_release_version "$tag_matches" || return 1
  VERSION=$tag_matches
}

validate_release_version() {
  if ! printf '%s\n' "$1" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "错误：发布版本必须是 vX.Y.Z，实际为 $1" >&2
    return 1
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

select_latest_version() {
  case "$RELEASE_SOURCE" in
    github)
      github_latest_version
      ACTIVE_SOURCE=github
      ;;
    gitee)
      gitee_latest_version
      ACTIVE_SOURCE=gitee
      ;;
    auto)
      if github_latest_version; then
        ACTIVE_SOURCE=github
      else
        latest_status=$?
        [ "$latest_status" -eq 75 ] || return "$latest_status"
        echo "GitHub 发布源不可用，切换到 Gitee 镜像。"
        gitee_latest_version
        ACTIVE_SOURCE=gitee
      fi
      ;;
  esac
}

release_asset_url() {
  asset_source=$1
  asset_name=$2
  case "$asset_source" in
    github) printf 'https://github.com/%s/releases/download/%s/%s\n' "$GITHUB_REPO" "$VERSION" "$asset_name" ;;
    gitee) printf 'https://gitee.com/%s/releases/download/%s/%s\n' "$GITEE_REPO" "$VERSION" "$asset_name" ;;
  esac
}

download_verified_release() {
  pair_source=$1
  download_name=$FILENAME
  [ "$pair_source" != "gitee" ] || download_name="${FILENAME}.gz"
  asset_url=$(release_asset_url "$pair_source" "$download_name")
  checksum_url=$(release_asset_url "$pair_source" checksums.txt)
  echo "Downloading ${asset_url}..."
  : >"$TMP"
  : >"$ARCHIVE_TMP"
  : >"$CHECKSUM_TMP"
  if [ "$pair_source" = "gitee" ]; then
    command -v gzip >/dev/null 2>&1 || {
      echo "错误：从 Gitee 安装需要 gzip" >&2
      return 1
    }
    release_download "$pair_source" "$asset_url" "$ARCHIVE_TMP" || return $?
    gzip -t "$ARCHIVE_TMP" || {
      echo "错误：Gitee gzip 资产损坏：${download_name}" >&2
      return 1
    }
    gzip -dc "$ARCHIVE_TMP" | head -c 134217729 >"$TMP"
    unpacked_size=$(wc -c <"$TMP" | tr -d '[:space:]')
    if [ "$unpacked_size" -gt 134217728 ]; then
      echo "错误：Gitee gzip 资产解压后超过 128 MiB" >&2
      return 1
    fi
  else
    release_download "$pair_source" "$asset_url" "$TMP" || return $?
  fi
  release_download "$pair_source" "$checksum_url" "$CHECKSUM_TMP" || return $?
  verify_release_asset "$TMP" "$CHECKSUM_TMP" "$FILENAME"
}

absolute_file_path() {
  file_path=$1
  file_dir=${file_path%/*}
  file_name=${file_path##*/}
  absolute_dir=$(CDPATH= cd -- "$file_dir" 2>/dev/null && pwd) || return 1
  printf '%s/%s\n' "$absolute_dir" "$file_name"
}

# 生成可直接复制到 POSIX shell 的单个参数，避免安装路径中的空格或引号改变命令语义。
shell_quote() {
  quoted_value=$(printf '%s' "$1" | sed "s/'/'\\\\''/g")
  printf "'%s'\n" "$quoted_value"
}

run_dependency_setup() {
  installed_weclaw=$1
  quoted_weclaw=$(shell_quote "$installed_weclaw")
  # 兼容旧变量：曾显式禁止 Claude ACP 自动安装的调用方不得被新向导引入其他写操作。
  if [ "${WECLAW_SKIP_DEPENDENCY_SETUP:-0}" = "1" ] || [ "${WECLAW_SKIP_CLAUDE_ACP:-0}" = "1" ]; then
    return 0
  fi

  echo ""
  echo "正在检查运行依赖..."
  if ! "$installed_weclaw" doctor; then
    echo "检测到阻断项，可在下面的依赖向导中选择修复。"
  fi

  if doctor_help=$("$installed_weclaw" doctor --help 2>&1); then
    :
  else
    doctor_help=
  fi
  case "$doctor_help" in
    *--fix*) ;;
    *)
      echo "当前安装版本尚不支持依赖选择向导；请更新 WeClaw 后运行："
      echo "  $quoted_weclaw doctor --fix"
      return 0
      ;;
  esac

  if [ "${WECLAW_INSTALL_INTERACTIVE:-0}" = "1" ] || [ -t 0 ]; then
    "$installed_weclaw" doctor --fix
    return
  fi
  if [ -t 1 ] && [ -r /dev/tty ] && [ -w /dev/tty ]; then
    "$installed_weclaw" doctor --fix </dev/tty >/dev/tty 2>&1
    return
  fi

  echo "未检测到交互终端，未自动安装任何依赖。"
  echo "请在终端运行："
  echo "  $quoted_weclaw doctor --fix"
  echo "非交互环境可显式执行："
  echo "  $quoted_weclaw doctor --fix --components <组件列表> --yes"
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

case "$OS/$ARCH" in
  darwin/arm64|linux/arm64|linux/amd64) ;;
  *)
    echo "Unsupported release platform: $OS/$ARCH (published: darwin/arm64, linux/arm64, linux/amd64)" >&2
    exit 1
    ;;
esac

echo "Detected: ${OS}/${ARCH}"

# Download
FILENAME="${BINARY}_${OS}_${ARCH}"
VERSION="${WECLAW_VERSION:-latest}"
if [ "$VERSION" = "latest" ]; then
  select_latest_version
else
  validate_release_version "$VERSION"
  case "$RELEASE_SOURCE" in
    gitee) ACTIVE_SOURCE=gitee ;;
    *) ACTIVE_SOURCE=github ;;
  esac
fi
TMP=$(mktemp)
ARCHIVE_TMP=$(mktemp)
CHECKSUM_TMP=$(mktemp)
cleanup_downloads() {
  rm -f "$TMP" "$ARCHIVE_TMP" "$CHECKSUM_TMP"
}
trap cleanup_downloads 0
trap 'exit 1' 1 2 15
if download_verified_release "$ACTIVE_SOURCE"; then
  :
else
  download_status=$?
  if [ "$RELEASE_SOURCE" = "auto" ] && [ "$ACTIVE_SOURCE" = "github" ] && [ "$download_status" -eq 75 ]; then
    echo "GitHub 发布资产不可用，切换到 Gitee 镜像。"
    ACTIVE_SOURCE=gitee
    download_verified_release "$ACTIVE_SOURCE"
  else
    exit "$download_status"
  fi
fi

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

echo ""
echo "weclaw ${VERSION} installed to ${INSTALL_DIR}/${BINARY}"
run_dependency_setup "$INSTALLED_WECLAW"
echo ""
echo "Get started:"
echo "  weclaw doctor"
echo "  weclaw doctor --fix"
echo "  weclaw start"
