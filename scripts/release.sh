#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
DRY_RUN=0
TAG=""
RELEASE_TAG_CREATED=0
RELEASE_TAG_PUSHED=0
RELEASE_DRAFT_ATTEMPTED=0
RELEASE_COMMITTED=0

TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/arm64"
  "linux/amd64"
)

usage() {
  cat <<'EOF'
用法:
  scripts/release.sh v0.1.42
  scripts/release.sh --next-patch
  scripts/release.sh --next-patch --dry-run

选项:
  --next-patch   基于当前最大 vX.Y.Z tag 自动递增 patch 版本
  --dry-run      执行检查、测试和打包，但不创建 tag、不推送、不创建 release
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
  while (($# > 0)); do
    case "$1" in
      --next-patch)
        [[ -z "$TAG" ]] || fail "不能同时指定 tag 和 --next-patch"
        TAG="$(next_patch_tag)"
        ;;
      --dry-run)
        DRY_RUN=1
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
}

check_dependencies() {
  require_command git
  require_command go
  require_command gh
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
	mkdir -p "$out_dir"
	rm -f "$out_dir"/weclaw_* "$out_dir/checksums.txt"

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

cleanup_failed_release() {
	local exit_code=$?
	trap - EXIT
	if [[ "$exit_code" -eq 0 || "$DRY_RUN" -eq 1 || "$RELEASE_COMMITTED" -eq 1 ]]; then
		exit "$exit_code"
	fi

	printf '发布事务失败，正在清理未提交的 Release 和 tag：%s\n' "$TAG" >&2
	local cleanup_failed=0
	if [[ "$RELEASE_DRAFT_ATTEMPTED" -eq 1 ]]; then
		if ! gh release delete "$TAG" --repo TingRuDeng/weclaw --cleanup-tag --yes; then
			cleanup_failed=1
			if [[ "$RELEASE_TAG_PUSHED" -eq 1 ]] && ! git push origin --delete "$TAG"; then
				cleanup_failed=1
			fi
		fi
	elif [[ "$RELEASE_TAG_PUSHED" -eq 1 ]]; then
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
}

verify_release_assets() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	local expected_draft="$1" asset_count assets expected_asset expected_asset_count release_info release_tag is_draft is_prerelease target
	log "验证 GitHub Release 资产"
	release_info="$(gh release view "$TAG" --repo TingRuDeng/weclaw --json tagName,isDraft,isPrerelease --jq '[.tagName, (.isDraft | tostring), (.isPrerelease | tostring)] | @tsv')"
	IFS=$'\t' read -r release_tag is_draft is_prerelease <<<"$release_info"
	[[ "$release_tag" == "$TAG" ]] || fail "Release tag 为 $release_tag，期望 $TAG"
	[[ "$is_draft" == "$expected_draft" ]] || fail "Release draft 状态为 $is_draft，期望 $expected_draft：$TAG"
	[[ "$is_prerelease" == "false" ]] || fail "Release 仍是 prerelease：$TAG"
	asset_count="$(gh release view "$TAG" --repo TingRuDeng/weclaw --json assets --jq '.assets | length')"
	expected_asset_count=$(( ${#TARGETS[@]} + 1 ))
	[[ "$asset_count" == "$expected_asset_count" ]] || fail "Release 资产数量异常：$asset_count，期望 $expected_asset_count"
	assets="$(gh release view "$TAG" --repo TingRuDeng/weclaw --json assets --jq '.assets[].name')"
	for target in "${TARGETS[@]}"; do
		expected_asset="weclaw_${target//\//_}"
		grep -Fxq "$expected_asset" <<<"$assets" || fail "Release 缺少资产：$expected_asset"
	done
	grep -Fxq "checksums.txt" <<<"$assets" || fail "Release 缺少资产：checksums.txt"

	(
		set -euo pipefail
		local checksum_count checksum_names tmp_dir
		tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/weclaw-release-verify.XXXXXX")"
		cleanup() {
			rm -rf "$tmp_dir"
		}
		trap cleanup EXIT

		gh release download "$TAG" --repo TingRuDeng/weclaw --dir "$tmp_dir"
		[[ -f "$tmp_dir/checksums.txt" ]] || fail "下载的 Release 缺少 checksums.txt"
		checksum_names="$(awk 'NF >= 2 { name=$2; sub(/^\\*/, "", name); print name }' "$tmp_dir/checksums.txt")"
		checksum_count="$(awk 'NF >= 2 { count++ } END { print count+0 }' "$tmp_dir/checksums.txt")"
		[[ "$checksum_count" == "${#TARGETS[@]}" ]] || fail "checksums.txt 条目数异常：$checksum_count，期望 ${#TARGETS[@]}"
		for target in "${TARGETS[@]}"; do
			expected_asset="weclaw_${target//\//_}"
			[[ -f "$tmp_dir/$expected_asset" ]] || fail "下载的 Release 缺少资产：$expected_asset"
			grep -Fxq "$expected_asset" <<<"$checksum_names" || fail "checksums.txt 缺少资产：$expected_asset"
		done
		if ! (cd "$tmp_dir" && shasum -a 256 -c checksums.txt); then
			fail "Release 资产 checksum 校验失败"
		fi
	)
}

promote_release() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	log "发布正式 latest Release：$TAG"
	gh release edit "$TAG" --repo TingRuDeng/weclaw --draft=false --latest
}

verify_release() {
	[[ "$DRY_RUN" -eq 0 ]] || return 0
	local latest_tag
	verify_release_assets false
	latest_tag="$(gh release view --repo TingRuDeng/weclaw --json tagName --jq '.tagName')"
	[[ "$latest_tag" == "$TAG" ]] || fail "latest release 指向 $latest_tag，期望 $TAG"
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
		local github_token tmp_dir smoke_bin version_output
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
		go build -trimpath -ldflags="-s -w -X github.com/fastclaw-ai/weclaw/cmd.Version=v0.0.0-update-smoke" -o "$smoke_bin" .
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
  check_release_source
  check_tag_available
  configure_go_cache
  run_validations
	build_assets
	trap cleanup_failed_release EXIT
	stage_release
	verify_release_assets true
	verify_update_smoke
	promote_release
	verify_release
	RELEASE_COMMITTED=1
	trap - EXIT
	log "发布完成：$TAG"
}

if [[ "${WECLAW_RELEASE_SOURCE_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
