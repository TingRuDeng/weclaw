#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
DRY_RUN=0
TAG=""
MODE=""
INSTALL=0
PACKAGE_COMMIT=""
RELEASE_TAG_CREATED=0
RELEASE_TAG_PUSHED=0
RELEASE_DRAFT_ATTEMPTED=0
RELEASE_COMMITTED=0
RELEASE_ID=""

TARGETS=(
  "darwin/arm64"
  "linux/amd64"
)

usage() {
  cat <<'EOF'
用法:
  scripts/release.sh package --next-patch
  scripts/release.sh package v0.1.42 --install
  scripts/release.sh publish v0.1.42
  scripts/release.sh publish v0.1.42 --dry-run

选项:
  package       完整验证并打包，不访问发布服务、不推送；同提交的已封装包可复用
  publish       验证并上传已有包，不重新构建发布资产
  --next-patch  打包时基于本地最大 vX.Y.Z tag 自动递增 patch
  --install     打包后安全更新 PATH 中的本机 weclaw，不自动重启
  --dry-run     只验证发布前置条件，不创建 tag、不上传
  -h, --help     显示帮助

环境:
  WECLAW_GOCACHE 优先指定本项目的持久化 Go 构建缓存
  GOCACHE        未设置 WECLAW_GOCACHE 时保留调用方显式导出的缓存
EOF
}

log() {
  printf '\n==> %s\n' "$*"
}

fail() {
  printf '发布失败：%s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

configure_go_cache() {
  local cache_dir cache_source host_os shared_root
  host_os="${1:-}"
  shared_root="${2:-/Volumes/Data/AppData/BuildCaches}"

  if [[ -n "${WECLAW_GOCACHE:-}" ]]; then
    cache_dir="$WECLAW_GOCACHE"
    cache_source="WECLAW_GOCACHE"
  elif [[ -n "${GOCACHE:-}" ]]; then
    cache_dir="$GOCACHE"
    cache_source="GOCACHE"
  else
    [[ -n "$host_os" ]] || host_os="$(go env GOHOSTOS)"
    if [[ "$host_os" == "darwin" && -d "$shared_root" ]]; then
      [[ -w "$shared_root" ]] || fail "WeClaw 共享缓存根目录不可写：$shared_root"
      cache_dir="$shared_root/weclaw"
      cache_source="WeClaw Darwin 共享缓存"
    else
      cache_dir="$(go env GOCACHE)"
      cache_source="Go 默认缓存"
    fi
  fi

  [[ -n "$cache_dir" && "$cache_dir" == /* ]] || fail "Go 构建缓存必须是绝对路径：${cache_dir:-<empty>}"
  mkdir -p "$cache_dir" || fail "无法创建 Go 构建缓存：$cache_dir"
  [[ -d "$cache_dir" && -w "$cache_dir" ]] || fail "Go 构建缓存不可写：$cache_dir"
  export GOCACHE="$cache_dir"
  log "Go 构建缓存：${GOCACHE}（${cache_source}）"
}

latest_version_tag() {
  git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n 1
}

next_patch_tag() {
  local latest major minor patch
  latest="$(latest_version_tag)"
  [[ "$latest" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] || fail "找不到可递增的语义化 tag"
  major="${BASH_REMATCH[1]}"
  minor="${BASH_REMATCH[2]}"
  patch="${BASH_REMATCH[3]}"
  printf 'v%s.%s.%s\n' "$major" "$minor" "$((patch + 1))"
}

parse_args() {
  case "${1:-}" in
    package|publish) MODE="$1"; shift ;;
  esac
  while (($# > 0)); do
    case "$1" in
      --next-patch)
        [[ -z "$TAG" ]] || fail "不能同时指定 tag 和 --next-patch"
        TAG="$(next_patch_tag)"
        ;;
      --dry-run)
        DRY_RUN=1
        ;;
      --install)
        INSTALL=1
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      v[0-9]*.[0-9]*.[0-9]*)
        [[ -z "$TAG" ]] || fail "只能指定一个发布 tag"
        TAG="$1"
        ;;
      *)
        fail "未知参数：$1"
        ;;
    esac
    shift
  done
  [[ -n "$TAG" ]] || fail "必须指定 tag 或 --next-patch"
  [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "tag 必须形如 v0.1.42"
  [[ "$MODE" == package || "$MODE" == publish ]] || fail "请显式选择 package 或 publish"
  [[ "$INSTALL" -eq 0 || "$MODE" == package ]] || fail "--install 仅用于 package"
  [[ "$DRY_RUN" -eq 0 || "$MODE" == publish ]] || fail "--dry-run 仅用于 publish"
}

check_dependencies() {
  require_command git
  require_command go
  [[ "$MODE" != publish ]] || require_command gh
  require_command python3
  require_command shasum
}

check_clean_tree() {
	local dirty
	dirty="$(git status --short --untracked-files=all | grep -v '^?? dist/' || true)"
	if [[ -n "$dirty" ]]; then
		printf '发布失败：工作区存在未提交改动，请先提交：\n%s\n' "$dirty" >&2
		exit 1
	fi
}

check_release_source() {
  local branch head remote_main
  branch="$(git branch --show-current)"
  [[ "$branch" == "main" ]] || fail "正式发布只能从 main 分支执行，当前分支：${branch:-detached HEAD}"

  log "核对本地 main 与 origin/main"
  git fetch --quiet origin main
  head="$(git rev-parse HEAD)"
  remote_main="$(git rev-parse FETCH_HEAD)"
  [[ "$head" == "$remote_main" ]] || fail "本地 HEAD ($head) 与 origin/main ($remote_main) 不一致，请先完成主分支同步"
}

check_tag_available() {
	git rev-parse -q --verify "refs/tags/$TAG" >/dev/null && fail "本地 tag 已存在：$TAG"
  if git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
    fail "远端 tag 已存在：$TAG"
  fi
}

run_validations() {
  local packages
  log "运行测试与静态检查"
  sh "$ROOT_DIR/scripts/install_test.sh"
  python3 "$ROOT_DIR/scripts/validate_docs.py" . --profile generic
  go mod tidy -diff
  packages="$(go list ./...)"
  [[ -n "$packages" ]] || fail "go list ./... 未找到任何包，拒绝跳过测试"
  go test -count=1 -timeout 120s ./...
  go test -race -count=1 -timeout 180s ./...
  go vet ./...
  go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
  go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
  git diff --check
}

build_assets() {
	local out_dir="$DIST_DIR/$TAG"
	log "构建发布资产：$out_dir"
	[[ ! -e "$out_dir" ]] || fail "包目录已存在但未通过封装验证，请检查并移走后重新打包：$out_dir"
	mkdir -p "$out_dir"

	local target goos goarch ext output
	for target in "${TARGETS[@]}"; do
    goos="${target%/*}"
    goarch="${target#*/}"
    ext=""
    [[ "$goos" == "windows" ]] && ext=".exe"
    output="$out_dir/weclaw_${goos}_${goarch}${ext}"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w -X github.com/fastclaw-ai/weclaw/cmd.Version=$TAG" -o "$output" .
	done

	# update 命令依赖 checksums.txt 校验资产完整性，本地和 Actions 产物必须保持同名格式。
	(cd "$out_dir" && shasum -a 256 weclaw_* > checksums.txt)
}

write_package_manifest() {
  python3 "$ROOT_DIR/scripts/release_package.py" seal "$DIST_DIR/$TAG" "$TAG" "$PACKAGE_COMMIT"
}

verify_package() {
  python3 "$ROOT_DIR/scripts/release_package.py" verify "$DIST_DIR/$TAG" "$TAG" "$(git rev-parse HEAD)"
}

install_package() {
  local installed host_os host_arch
  installed="$(command -v weclaw)" || fail "PATH 中未找到已有 weclaw 安装"
  [[ "$installed" == /* && -f "$installed" ]] || fail "本地安装目标必须是 PATH 中的可执行文件"
  host_os="$(go env GOHOSTOS)"
  host_arch="$(go env GOHOSTARCH)"
  release_target_supported "$host_os/$host_arch" || fail "当前主机不在本地安装矩阵中"
  verify_package
  "$DIST_DIR/$TAG/weclaw_${host_os}_${host_arch}" update --from-package "$DIST_DIR/$TAG" --target "$installed"
}

package_release() {
  PACKAGE_COMMIT="$(git rev-parse HEAD)"
  if [[ -e "$DIST_DIR/$TAG.package.json" ]]; then
    verify_package
    log "复用已验证包：$TAG"
  else
    configure_go_cache
    run_validations
    build_assets
    check_clean_tree
    [[ "$(git rev-parse HEAD)" == "$PACKAGE_COMMIT" ]] || fail "打包期间提交已变化，拒绝封装"
    write_package_manifest
    verify_package
  fi
  [[ "$INSTALL" -eq 0 ]] || install_package
  log "打包完成：$DIST_DIR/${TAG}；真机验证后执行 scripts/release.sh publish $TAG"
}

query_release_id() {
	gh api --paginate "repos/TingRuDeng/weclaw/releases?per_page=100" \
		--jq ".[] | select(.tag_name == \"$TAG\") | .id"
}

resolve_release_id() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	local attempt candidate
	for attempt in 1 2 3 4 5 6 7 8 9 10; do
		if candidate="$(query_release_id)" && [[ "$candidate" =~ ^[0-9]+$ ]]; then
			RELEASE_ID="${candidate%%$'\n'*}"
			return 0
		fi
		sleep 1
	done
	fail "无法通过认证 Release 列表找到草稿：$TAG"
}

cleanup_failed_release() {
	local exit_code=$?
	trap - EXIT
	if [[ "$exit_code" -eq 0 || "$DRY_RUN" -eq 1 || "$RELEASE_COMMITTED" -eq 1 ]]; then
		exit "$exit_code"
	fi

	printf '发布事务失败，正在清理未提交的 Release 和 tag：%s\n' "$TAG" >&2
	local cleanup_failed=0 cleanup_release_id="$RELEASE_ID"
	if [[ "$RELEASE_DRAFT_ATTEMPTED" -eq 1 ]]; then
		if [[ -z "$cleanup_release_id" ]]; then
			cleanup_release_id="$(query_release_id 2>/dev/null || true)"
			cleanup_release_id="${cleanup_release_id%%$'\n'*}"
		fi
		if [[ "$cleanup_release_id" =~ ^[0-9]+$ ]]; then
			if ! gh api --method DELETE "repos/TingRuDeng/weclaw/releases/$cleanup_release_id"; then
				cleanup_failed=1
			fi
		elif ! gh release delete "$TAG" --repo TingRuDeng/weclaw --yes; then
			cleanup_failed=1
		fi
	fi
	if [[ "$RELEASE_TAG_PUSHED" -eq 1 ]]; then
		if ! git push origin --delete "$TAG"; then
			cleanup_failed=1
		fi
	fi
	if [[ "$RELEASE_TAG_CREATED" -eq 1 ]]; then
		git tag -d "$TAG" >/dev/null 2>&1 || cleanup_failed=1
	fi
	if [[ "$cleanup_failed" -ne 0 ]]; then
		printf '自动清理未完全成功，请人工核对远端 Release/tag：%s\n' "$TAG" >&2
	fi
	exit "$exit_code"
}

stage_release() {
	local out_dir="$DIST_DIR/$TAG"
	[[ "$DRY_RUN" -eq 0 ]] || {
		log "dry-run：跳过 tag 推送和 GitHub draft Release 创建"
		return 0
	}

	log "创建并推送暂存 tag：$TAG"
	git tag "$TAG"
	RELEASE_TAG_CREATED=1
	git push origin "$TAG"
	RELEASE_TAG_PUSHED=1

	# 先以 draft 暂存，远端资产和 update smoke 全部通过后才公开为 latest。
	log "创建 GitHub draft Release：$TAG"
	# gh 可能先创建 draft 再在资产上传阶段失败，因此尝试创建前就开启清理分支。
	RELEASE_DRAFT_ATTEMPTED=1
	gh release create "$TAG" "$out_dir"/weclaw_* "$out_dir/checksums.txt" \
		--repo TingRuDeng/weclaw \
		--draft \
		--verify-tag \
		--title "$TAG" \
		--generate-notes
	resolve_release_id
}

verify_release_assets() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	local expected_draft="$1" asset_count assets expected_asset expected_asset_count release_info release_tag is_draft is_prerelease target
	log "验证 GitHub Release 资产"
	[[ -n "$RELEASE_ID" ]] || fail "GitHub Release ID 尚未解析：$TAG"
	release_info="$(gh api "repos/TingRuDeng/weclaw/releases/$RELEASE_ID" --jq '[.tag_name, (.draft | tostring), (.prerelease | tostring)] | @tsv')"
	IFS=$'\t' read -r release_tag is_draft is_prerelease <<<"$release_info"
	[[ "$release_tag" == "$TAG" ]] || fail "Release tag 为 $release_tag，期望 $TAG"
	[[ "$is_draft" == "$expected_draft" ]] || fail "Release draft 状态为 $is_draft，期望 $expected_draft：$TAG"
	[[ "$is_prerelease" == "false" ]] || fail "Release 仍是 prerelease：$TAG"
	asset_count="$(gh api "repos/TingRuDeng/weclaw/releases/$RELEASE_ID" --jq '.assets | length')"
	expected_asset_count=$(( ${#TARGETS[@]} + 1 ))
	[[ "$asset_count" == "$expected_asset_count" ]] || fail "Release 资产数量异常：${asset_count}，期望 ${expected_asset_count}"
	assets="$(gh api "repos/TingRuDeng/weclaw/releases/$RELEASE_ID" --jq '.assets[].name')"
	for target in "${TARGETS[@]}"; do
		expected_asset="weclaw_${target//\//_}"
		grep -Fxq "$expected_asset" <<<"$assets" || fail "Release 缺少资产：$expected_asset"
	done
	grep -Fxq "checksums.txt" <<<"$assets" || fail "Release 缺少资产：checksums.txt"
}

promote_release() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	log "发布正式 latest Release：$TAG"
	[[ -n "$RELEASE_ID" ]] || fail "GitHub Release ID 尚未解析：$TAG"
	gh api --method PATCH "repos/TingRuDeng/weclaw/releases/$RELEASE_ID" \
		-F draft=false \
		-f make_latest=true >/dev/null
}

verify_release() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	local latest_tag
	verify_release_assets false
	latest_tag="$(gh release view --repo TingRuDeng/weclaw --json tagName --jq '.tagName')"
	[[ "$latest_tag" == "$TAG" ]] || fail "latest release 指向 $latest_tag，期望 $TAG"
}

mirror_gitee_release() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	log "镜像已验证资产到 Gitee：$TAG"
	"$ROOT_DIR/scripts/mirror_gitee_release.sh" "$TAG" "$DIST_DIR/$TAG"
}

release_target_supported() {
  local candidate="$1" target
  for target in "${TARGETS[@]}"; do
    [[ "$candidate" == "$target" ]] && return 0
  done
  return 1
}

verify_update_smoke() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0

	local host_os host_arch
	host_os="$(go env GOHOSTOS)"
	host_arch="$(go env GOHOSTARCH)"
	if ! release_target_supported "$host_os/$host_arch"; then
		log "跳过 update smoke：当前主机 ${host_os}/${host_arch} 不在正式发布矩阵中"
		return 0
	fi

	(
		set -euo pipefail
		local github_token tmp_dir smoke_bin version_output previous filename
		tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/weclaw-update-smoke.XXXXXX")"
		cleanup() {
			rm -rf "$tmp_dir"
		}
		trap cleanup EXIT

		log "验证 weclaw update 自更新链路"
		github_token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
		if [[ -z "$github_token" ]]; then
			github_token="$(gh auth token)"
		fi
		[[ -n "$github_token" ]] || fail "无法取得 GitHub 凭据，不能验证 draft Release 的 update 链路"
		smoke_bin="$tmp_dir/weclaw"
		mkdir -p "$tmp_dir/home"
		previous="$(gh release view --repo TingRuDeng/weclaw --json tagName --jq '.tagName')"
		[[ "$previous" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && "$previous" != "$TAG" ]] || fail "找不到可用于自更新验证的上一正式版"
		filename="weclaw_${host_os}_${host_arch}"
		gh release download "$previous" --repo TingRuDeng/weclaw --pattern "$filename" --pattern checksums.txt --dir "$tmp_dir"
		(cd "$tmp_dir" && shasum -a 256 --ignore-missing -c checksums.txt) || fail "上一正式版摘要校验失败"
		mv "$tmp_dir/$filename" "$smoke_bin"
		chmod 755 "$smoke_bin"
		env -u WECLAW_DAEMON_CHILD -u WECLAW_DAEMON_CLAUDE_PREFLIGHT \
			GITHUB_TOKEN="$github_token" WECLAW_HOME="$tmp_dir/home" WECLAW_UPDATE_RELEASE_TAG="$TAG" "$smoke_bin" update
		version_output="$(WECLAW_HOME="$tmp_dir/home" "$smoke_bin" version)"
		[[ "$version_output" == *"weclaw $TAG ("* ]] || fail "update smoke 版本异常：$version_output"
	)
}

main() {
  cd "$ROOT_DIR"
  parse_args "$@"
  check_dependencies
  check_clean_tree
  if [[ "$MODE" == package ]]; then
    package_release
    return
  fi
  verify_package
  check_release_source
  check_tag_available
	[[ "$DRY_RUN" -eq 0 ]] || { log "dry-run：包与发布来源验证通过，未上传"; return; }
	check_clean_tree
	verify_package
	trap cleanup_failed_release EXIT
	stage_release
	verify_release_assets true
	verify_update_smoke
	promote_release
	verify_release
	RELEASE_COMMITTED=1
	trap - EXIT
	mirror_gitee_release
	log "发布完成：$TAG"
}

if [[ "${WECLAW_RELEASE_SOURCE_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
