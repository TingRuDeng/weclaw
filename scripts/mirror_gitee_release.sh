#!/usr/bin/env bash
set -euo pipefail

GITEE_REPO="${GITEE_REPO:-jimdeng891/weclaw}"
GITEE_API_BASE="${GITEE_API_BASE:-https://gitee.com/api/v5}"
GITEE_WEB_BASE="${GITEE_WEB_BASE:-https://gitee.com}"
GITEE_CURL_MAX_TIME="${GITEE_CURL_MAX_TIME:-1800}"

usage() {
  cat <<'EOF'
用法:
  scripts/mirror_gitee_release.sh <vX.Y.Z> <asset-dir>

将当前 Git main/tag 和 asset-dir 中已由 GitHub 权威发布流程验证的资产镜像到 Gitee。

环境:
  GITEE_TOKEN  必需；仅从外部 Secret 注入，不写入仓库或命令参数
  GITEE_REPO   可选，默认 jimdeng891/weclaw
EOF
}

fail() {
  printf 'Gitee 镜像失败：%s\n' "$*" >&2
  exit 1
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi
[[ $# -eq 2 ]] || { usage >&2; exit 1; }

TAG="$1"
ASSET_DIR="$2"
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "tag 必须是 vX.Y.Z"
[[ -d "$ASSET_DIR" ]] || fail "asset-dir 不存在：$ASSET_DIR"
[[ -n "${GITEE_TOKEN:-}" ]] || fail "缺少 GITEE_TOKEN"
[[ "$GITEE_TOKEN" != *$'\n'* && "$GITEE_TOKEN" != *$'\r'* ]] || fail "GITEE_TOKEN 格式无效"
[[ "$GITEE_CURL_MAX_TIME" =~ ^[1-9][0-9]*$ ]] || fail "GITEE_CURL_MAX_TIME 必须是正整数秒"

for command_name in git curl python3 shasum gzip cp; do
  command -v "$command_name" >/dev/null 2>&1 || fail "缺少命令：$command_name"
done

SOURCE_ASSETS=(
  weclaw_darwin_arm64
  weclaw_linux_arm64
  weclaw_linux_amd64
)
GITEE_BINARY_ASSETS=(
  weclaw_darwin_arm64
  weclaw_linux_amd64
)
EXPECTED_ASSETS=(checksums.txt)
for asset_name in "${GITEE_BINARY_ASSETS[@]}"; do
  EXPECTED_ASSETS+=("${asset_name}.gz")
done

actual_count="$(find "$ASSET_DIR" -maxdepth 1 -type f | wc -l | tr -d '[:space:]')"
[[ "$actual_count" == "$((${#SOURCE_ASSETS[@]} + 1))" ]] || fail "asset-dir 文件数为 $actual_count，期望 $((${#SOURCE_ASSETS[@]} + 1))"
for asset_name in "${SOURCE_ASSETS[@]}" checksums.txt; do
  [[ -f "$ASSET_DIR/$asset_name" ]] || fail "缺少资产：$asset_name"
done
(cd "$ASSET_DIR" && shasum -a 256 -c checksums.txt) >/dev/null || fail "本地资产摘要校验失败"

umask 077
TEMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/weclaw-gitee-mirror.XXXXXX")"
cleanup() {
  rm -rf "$TEMP_DIR"
}
trap cleanup EXIT

MIRROR_DIR="$TEMP_DIR/mirror-assets"
mkdir -p "$MIRROR_DIR"
for asset_name in "${GITEE_BINARY_ASSETS[@]}"; do
  gzip -n -9 -c "$ASSET_DIR/$asset_name" >"$MIRROR_DIR/$asset_name.gz"
done
cp "$ASSET_DIR/checksums.txt" "$MIRROR_DIR/checksums.txt"

TOKEN_FILE="$TEMP_DIR/token"
ASKPASS_FILE="$TEMP_DIR/askpass.sh"
printf '%s' "$GITEE_TOKEN" >"$TOKEN_FILE"
cat >"$ASKPASS_FILE" <<'EOF'
#!/bin/sh
case "$1" in
  *Username*) printf '%s\n' "${GITEE_USERNAME:?}" ;;
  *Password*) printf '%s\n' "${GITEE_TOKEN:?}" ;;
  *) exit 1 ;;
esac
EOF
chmod 700 "$ASKPASS_FILE"

GITEE_OWNER="${GITEE_REPO%%/*}"
export GITEE_USERNAME="${GITEE_USERNAME:-$GITEE_OWNER}"
export GIT_ASKPASS="$ASKPASS_FILE"
export GIT_TERMINAL_PROMPT=0
CURL_SECURE=(--connect-timeout 30 --max-time "$GITEE_CURL_MAX_TIME" --proto '=https' --tlsv1.2)

printf '==> 同步 main 和 %s 到 Gitee\n' "$TAG"
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || fail "本地 tag 不存在：$TAG"
gitee_git_url="${GITEE_WEB_BASE}/${GITEE_REPO}.git"
git push "$gitee_git_url" HEAD:refs/heads/main "refs/tags/$TAG:refs/tags/$TAG"
local_head="$(git rev-parse HEAD)"
local_tag="$(git rev-parse "refs/tags/$TAG")"
remote_refs="$(git ls-remote "$gitee_git_url" refs/heads/main "refs/tags/$TAG")"
remote_main="$(awk '$2 == "refs/heads/main" { print $1 }' <<<"$remote_refs")"
remote_tag="$(awk -v ref="refs/tags/$TAG" '$2 == ref { print $1 }' <<<"$remote_refs")"
[[ "$remote_main" == "$local_head" ]] || fail "Gitee main 未同步到当前提交"
[[ "$remote_tag" == "$local_tag" ]] || fail "Gitee tag 未同步到当前 tag"

release_json="$TEMP_DIR/release.json"
probe_status="$(curl -sS "${CURL_SECURE[@]}" --get \
  --data-urlencode "access_token@${TOKEN_FILE}" \
  -o "$release_json" -w '%{http_code}' \
  "${GITEE_API_BASE}/repos/${GITEE_REPO}/releases/tags/${TAG}")"
create_release=false
case "$probe_status" in
  200)
    release_probe_state="$(python3 - "$release_json" "$TAG" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    release = json.load(handle)
if release is None:
    print("missing")
elif isinstance(release, dict) and release.get("tag_name") == sys.argv[2] and release.get("id"):
    print("existing")
else:
    print("invalid")
PY
)" || fail "无法解析 Gitee Release 查询结果"
    case "$release_probe_state" in
      existing)
        printf '==> 复用已有 Gitee Release：%s\n' "$TAG"
        ;;
      missing)
        create_release=true
        ;;
      *)
        fail "Gitee Release 查询结果无效"
        ;;
    esac
    ;;
  404)
    create_release=true
    ;;
  *)
    fail "查询 Gitee Release 返回 HTTP ${probe_status}"
    ;;
esac
if [[ "$create_release" == true ]]; then
  printf '==> 创建 Gitee Release：%s\n' "$TAG"
  curl -fsS "${CURL_SECURE[@]}" \
    --form "access_token=<${TOKEN_FILE}" \
    --form-string "tag_name=$TAG" \
    --form-string "target_commitish=main" \
    --form-string "name=$TAG" \
    --form-string "body=GitHub 权威 Release $TAG 的校验后镜像。" \
    --form-string "prerelease=false" \
    -o "$release_json" \
    "${GITEE_API_BASE}/repos/${GITEE_REPO}/releases"
fi

release_id="$(python3 - "$release_json" "$TAG" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    release = json.load(handle)
if release.get("tag_name") != sys.argv[2] or not release.get("id"):
    raise SystemExit("Gitee release response tag/id invalid")
print(release["id"])
PY
)" || fail "无法确认 Gitee Release"

attachment_json="$TEMP_DIR/attachments.json"
curl -fsS "${CURL_SECURE[@]}" --get \
  --data-urlencode "access_token@${TOKEN_FILE}" \
  -o "$attachment_json" \
  "${GITEE_API_BASE}/repos/${GITEE_REPO}/releases/${release_id}/attach_files"
python3 - "$attachment_json" "$TEMP_DIR/existing-assets" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    attachments = json.load(handle)
with open(sys.argv[2], "w", encoding="utf-8") as output:
    for attachment in attachments:
        name = attachment.get("name", "")
        if name:
            output.write(name + "\n")
PY

for asset_name in "${EXPECTED_ASSETS[@]}"; do
  if grep -Fxq "$asset_name" "$TEMP_DIR/existing-assets"; then
    printf '==> 已存在 Gitee 资产，跳过上传：%s\n' "$asset_name"
    continue
  fi
  printf '==> 上传 Gitee 资产：%s\n' "$asset_name"
  curl -fsS "${CURL_SECURE[@]}" \
    --form "access_token=<${TOKEN_FILE}" \
    --form "file=@${MIRROR_DIR}/${asset_name}" \
    -o "$TEMP_DIR/upload-${asset_name}.json" \
    "${GITEE_API_BASE}/repos/${GITEE_REPO}/releases/${release_id}/attach_files"
done

printf '==> 核对 Gitee Release 资产清单\n'
curl -fsS "${CURL_SECURE[@]}" --get \
  --data-urlencode "access_token@${TOKEN_FILE}" \
  -o "$attachment_json" \
  "${GITEE_API_BASE}/repos/${GITEE_REPO}/releases/${release_id}/attach_files"

python3 - "$attachment_json" "$TEMP_DIR/assets.txt" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    assets = json.load(handle)
seen = set()
with open(sys.argv[2], "w", encoding="utf-8") as output:
    for asset in assets:
        name = asset.get("name", "")
        if not name or name in seen or any(char in name for char in "\t\r\n"):
            raise SystemExit("Gitee release contains invalid or duplicate asset name")
        seen.add(name)
        output.write(name + "\n")
PY

asset_count="$(wc -l <"$TEMP_DIR/assets.txt" | tr -d '[:space:]')"
[[ "$asset_count" == "${#EXPECTED_ASSETS[@]}" ]] || fail "Gitee Release 资产数为 ${asset_count}，期望 ${#EXPECTED_ASSETS[@]}"
for asset_name in "${EXPECTED_ASSETS[@]}"; do
  grep -Fxq "$asset_name" "$TEMP_DIR/assets.txt" || fail "Gitee Release 缺少资产：$asset_name"
done

printf 'Gitee 镜像完成：%s\n' "$TAG"
