package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/fastclaw-ai/weclaw/config"
)

func localUpdateFixture(t *testing.T, version string) (string, string, []byte) {
	t.Helper()
	t.Setenv("WECLAW_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), version)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(fmt.Sprintf("#!/bin/sh\necho 'weclaw %s (%s/%s)'\n", version, runtime.GOOS, runtime.GOARCH))
	hashes := map[string]string{}
	checksums := ""
	for _, name := range []string{"weclaw_darwin_arm64", "weclaw_linux_amd64"} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o755); err != nil {
			t.Fatal(err)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(data))
		checksums += hashes[name] + "  " + name + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "checksums.txt"), []byte(checksums), 0o644); err != nil {
		t.Fatal(err)
	}
	hashes["checksums.txt"] = fmt.Sprintf("%x", sha256.Sum256([]byte(checksums)))
	manifest, err := json.Marshal(map[string]any{"schema": 1, "version": version, "commit": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "assets": hashes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+".package.json", manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "weclaw")
	old := []byte(fmt.Sprintf("#!/bin/sh\necho 'weclaw v1.0.0 (%s/%s)'\n", runtime.GOOS, runtime.GOARCH))
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, target, old
}

func TestLocalPackageUpdateInstallsAndRollsBack(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			dir, target, old := localUpdateFixture(t, "v1.0.1")
			preflight := false
			ops := updateCompletionOps{prepare: func(context.Context) (preparedStart, error) {
				preflight = true
				if fail {
					return preparedStart{}, errors.New("preflight failed")
				}
				return preparedStart{cfg: &config.Config{}}, nil
			}, out: io.Discard}
			err := installLocalPackage(context.Background(), dir, target, ops, io.Discard)
			if (err != nil) != fail || !preflight {
				t.Fatalf("err=%v preflight=%v", err, preflight)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if fail && string(got) != string(old) {
				t.Fatal("failed local update did not restore old binary")
			}
			if !fail && string(got) == string(old) {
				t.Fatal("local update did not install package")
			}
		})
	}
}

func TestLocalPackageUpdateRejectsTamperingAndDowngrade(t *testing.T) {
	for _, test := range []string{"tampered", "downgrade", "symlink", "version mismatch"} {
		t.Run(test, func(t *testing.T) {
			version := "v1.0.1"
			if test == "downgrade" {
				version = "v0.9.0"
			}
			dir, target, old := localUpdateFixture(t, version)
			asset := filepath.Join(dir, "weclaw_"+runtime.GOOS+"_"+runtime.GOARCH)
			if test == "tampered" {
				if err := os.WriteFile(asset, []byte("changed"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if test == "symlink" {
				if err := os.Remove(asset); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, asset); err != nil {
					t.Fatal(err)
				}
			}
			if test == "version mismatch" {
				path := dir + ".package.json"
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var value map[string]any
				if err := json.Unmarshal(data, &value); err != nil {
					t.Fatal(err)
				}
				value["version"] = "v1.0.2"
				data, err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ops := updateCompletionOps{prepare: func(context.Context) (preparedStart, error) {
				t.Fatal("invalid package reached preflight")
				return preparedStart{}, nil
			}, out: io.Discard}
			if err := installLocalPackage(context.Background(), dir, target, ops, io.Discard); err == nil {
				t.Fatal("invalid local update accepted")
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(old) {
				t.Fatal("invalid package changed installed binary")
			}
		})
	}
}

func TestLocalPackageUpdateSameVersionUsesDigest(t *testing.T) {
	dir, target, _ := localUpdateFixture(t, "v1.0.0")
	// A rebuilt candidate can retain its unreleased version while changing bytes.
	if err := os.WriteFile(target, []byte(fmt.Sprintf("#!/bin/sh\n# old candidate\necho 'weclaw v1.0.0 (%s/%s)'\n", runtime.GOOS, runtime.GOARCH)), 0o755); err != nil {
		t.Fatal(err)
	}
	preflights := 0
	ops := updateCompletionOps{prepare: func(context.Context) (preparedStart, error) {
		preflights++
		return preparedStart{cfg: &config.Config{}}, nil
	}, out: io.Discard}
	for i := 0; i < 2; i++ {
		if err := installLocalPackage(context.Background(), dir, target, ops, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	if preflights != 1 {
		t.Fatalf("preflights=%d, want one replacement then idempotent reuse", preflights)
	}
}

func TestLocalPackageUpdateRejectsTargetInsidePackage(t *testing.T) {
	dir, _, _ := localUpdateFixture(t, "v1.0.1")
	target := filepath.Join(dir, "weclaw_"+runtime.GOOS+"_"+runtime.GOARCH)
	if err := installLocalPackage(context.Background(), dir, target, updateCompletionOps{}, io.Discard); err == nil {
		t.Fatal("package asset accepted as install target")
	}
}
