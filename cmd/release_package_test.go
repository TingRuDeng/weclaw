package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePackageAndPublishAreSeparate(t *testing.T) {
	script := releaseScriptPath(t)
	for _, mode := range []string{"package", "publish"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			command := `WECLAW_RELEASE_SOURCE_ONLY=1 source ` + shellQuote(script) + `
ROOT_DIR=` + shellQuote(root) + `
check_dependencies() { :; }
check_clean_tree() { :; }
check_tag_available() { :; }
check_release_source() { echo remote-check; }
configure_go_cache() { :; }
run_validations() { echo validated; }
build_assets() { echo built; }
write_package_manifest() { echo sealed; }
verify_package() { echo verified; }
git() { if [[ "$1" == rev-parse ]]; then echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; else echo unexpected-git >&2; return 91; fi; }
stage_release() { echo uploaded; }
verify_release_assets() { :; }
verify_update_smoke() { :; }
promote_release() { :; }
verify_release() { :; }
mirror_gitee_release() { :; }
main ` + mode + ` v9.9.9`
			output := runReleaseScriptTestCommand(t, "", "bash", "-c", command)
			if mode == "package" {
				if !strings.Contains(output, "built") || !strings.Contains(output, "validated") || !strings.Contains(output, "sealed") || strings.Contains(output, "uploaded") || strings.Contains(output, "remote-check") {
					t.Fatalf("package crossed publish boundary: %s", output)
				}
			} else if strings.Contains(output, "built") || strings.Contains(output, "validated") || !strings.Contains(output, "verified") || !strings.Contains(output, "uploaded") {
				t.Fatalf("publish must reuse verified package: %s", output)
			}
		})
	}
}

func TestReleasePackageSealRejectsDriftAndTampering(t *testing.T) {
	script := releaseScriptPath(t)
	root := t.TempDir()
	assets := filepath.Join(root, "v9.9.9")
	if err := os.Mkdir(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"weclaw_darwin_arm64", "weclaw_linux_amd64"} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	setup := `WECLAW_RELEASE_SOURCE_ONLY=1 source ` + shellQuote(script) + `
DIST_DIR=` + shellQuote(root) + `
TAG=v9.9.9
PACKAGE_COMMIT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
git() { echo "$PACKAGE_COMMIT"; }
`
	runReleaseScriptTestCommand(t, assets, "bash", "-c", `shasum -a 256 weclaw_* > checksums.txt`)
	runReleaseScriptTestCommand(t, "", "bash", "-c", setup+`write_package_manifest; verify_package`)
	runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", setup+`PACKAGE_COMMIT=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb; verify_package`)
	if err := os.WriteFile(filepath.Join(assets, "weclaw_darwin_arm64"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", setup+`verify_package`)
}

func TestLocalPackageUpdateFlagsAreExplicit(t *testing.T) {
	for _, name := range []string{"from-package", "target"} {
		if updateCmd.Flags().Lookup(name) == nil {
			t.Errorf("local package update missing --%s", name)
		}
	}
}

func TestReleasePackageInstallOptionConsumesOneArgument(t *testing.T) {
	for _, args := range []string{"package v9.9.9 --install", "package --install v9.9.9"} {
		command := `WECLAW_RELEASE_SOURCE_ONLY=1 source ` + shellQuote(releaseScriptPath(t)) + ` && parse_args ` + args + ` && [[ "$INSTALL" == 1 && "$TAG" == v9.9.9 ]]`
		runReleaseScriptTestCommand(t, "", "bash", "-c", command)
	}
}

func TestReleaseUpdateSmokeUsesPreviousBinaryWithoutBuilding(t *testing.T) {
	for _, tampered := range []bool{false, true} {
		t.Run(fmt.Sprint(tampered), func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "previous")
			data := []byte("#!/bin/sh\nif [ \"$1\" = update ]; then echo updated >>\"$SMOKE_MARKER\"; else echo 'weclaw v9.9.9 (darwin/arm64)'; fi\n")
			if err := os.WriteFile(binary, data, 0o755); err != nil {
				t.Fatal(err)
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(data))
			if tampered {
				digest = strings.Repeat("0", 64)
			}
			marker := filepath.Join(root, "updated")
			command := `WECLAW_RELEASE_SOURCE_ONLY=1 source ` + shellQuote(releaseScriptPath(t)) + `
export SMOKE_MARKER=` + shellQuote(marker) + `
go() { case "$*" in 'env GOHOSTOS') echo darwin ;; 'env GOHOSTARCH') echo arm64 ;; *) echo unexpected-build >&2; return 91 ;; esac; }
gh() {
  case "$1 $2" in
    'auth token') echo test-token ;;
    'release view') echo v9.9.8 ;;
    'release download')
      [[ "$3" == v9.9.8 ]] || return 92
      local destination="${@: -1}"
      cp ` + shellQuote(binary) + ` "$destination/weclaw_darwin_arm64"
      printf '%s  weclaw_darwin_arm64\n' ` + shellQuote(digest) + ` > "$destination/checksums.txt"
      ;;
    *) return 93 ;;
  esac
}
TAG=v9.9.9 verify_update_smoke`
			if tampered {
				runReleaseScriptTestCommandExpectFailure(t, "", "bash", "-c", command)
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("unverified updater executed")
				}
			} else {
				runReleaseScriptTestCommand(t, "", "bash", "-c", command)
				if data, err := os.ReadFile(marker); err != nil || string(data) != "updated\n" {
					t.Fatalf("smoke result=%q err=%v", data, err)
				}
			}
		})
	}
}
