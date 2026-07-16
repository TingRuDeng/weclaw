#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="$ROOT_DIR/dist"
DRY_RUN=0
RUN_TESTS=1
TAG=""

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
  --skip-tests   跳过测试，仅用于已经完成同等验证的紧急发布
  -h, --help     显示帮助
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
      --skip-tests)
        RUN_TESTS=0
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
  [[ "$RUN_TESTS" -eq 1 ]] || return 0
  log "运行测试与静态检查"
  sh "$ROOT_DIR/scripts/install_test.sh"
  go mod tidy -diff
  go test -count=1 -timeout 60s ./...
  go test -race -count=1 -timeout 60s ./agent ./cmd ./messaging
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

create_release() {
	local out_dir="$DIST_DIR/$TAG"
  [[ "$DRY_RUN" -eq 0 ]] || {
    log "dry-run：跳过 tag 推送和 GitHub Release 创建"
    return 0
  }

	log "创建并推送 tag：$TAG"
	git tag "$TAG"
	git push origin "$TAG"

	# 正式 release 会被 weclaw update 识别为 latest，不能使用 prerelease。
	log "创建 GitHub Release：$TAG"
	gh release create "$TAG" "$out_dir"/weclaw_* "$out_dir/checksums.txt" \
		--repo TingRuDeng/weclaw \
    --title "$TAG" \
    --generate-notes
}

verify_release() {
  [[ "$DRY_RUN" -eq 0 ]] || return 0
  local asset_count assets expected_asset expected_asset_count latest_tag target
  log "验证 GitHub Release"
  asset_count="$(gh release view "$TAG" --repo TingRuDeng/weclaw --json assets --jq '.assets | length')"
  expected_asset_count=$(( ${#TARGETS[@]} + 1 ))
  [[ "$asset_count" == "$expected_asset_count" ]] || fail "Release 资产数量异常：$asset_count，期望 $expected_asset_count"
  assets="$(gh release view "$TAG" --repo TingRuDeng/weclaw --json assets --jq '.assets[].name')"
  for target in "${TARGETS[@]}"; do
    expected_asset="weclaw_${target//\//_}"
    grep -Fxq "$expected_asset" <<<"$assets" || fail "Release 缺少资产：$expected_asset"
  done
  grep -Fxq "checksums.txt" <<<"$assets" || fail "Release 缺少资产：checksums.txt"
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
    local tmp_dir smoke_bin version_output
    tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/weclaw-update-smoke.XXXXXX")"
    cleanup() {
      rm -rf "$tmp_dir"
    }
    trap cleanup EXIT

    log "验证 weclaw update 自更新链路"
    smoke_bin="$tmp_dir/weclaw"
    mkdir -p "$tmp_dir/home"
    go build -trimpath -ldflags="-s -w -X github.com/fastclaw-ai/weclaw/cmd.Version=v0.0.0-update-smoke" -o "$smoke_bin" .
    WECLAW_HOME="$tmp_dir/home" "$smoke_bin" update
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
  run_validations
  build_assets
  create_release
  verify_release
  verify_update_smoke
  log "发布完成：$TAG"
}

if [[ "${WECLAW_RELEASE_SOURCE_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
