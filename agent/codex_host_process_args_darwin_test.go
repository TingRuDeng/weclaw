//go:build darwin

package agent

import (
	"encoding/binary"
	"reflect"
	"testing"
)

func TestParseDarwinCodexHostArgsPreservesArgumentBoundaries(t *testing.T) {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, 3)
	data = append(data,
		[]byte("/opt/codex\x00\x00/opt/codex\x00-C\x00/tmp/path with spaces\x00TOKEN=secret\x00")...,
	)

	got, err := parseDarwinCodexHostArgs(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/codex", "-C", "/tmp/path with spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("args=%#v, want %#v", got, want)
	}
}

func TestParseDarwinCodexHostArgsRejectsTruncatedArgv(t *testing.T) {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, 2)
	data = append(data, []byte("/opt/codex\x00\x00/opt/codex\x00")...)

	if _, err := parseDarwinCodexHostArgs(data); err == nil {
		t.Fatal("truncated argv must fail closed")
	}
}

func TestParseDarwinProcessEnvironmentValueReturnsOnlyRequestedVariable(t *testing.T) {
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, 1)
	data = append(data, []byte(
		"/Applications/Codex.app/Contents/MacOS/ChatGPT\x00\x00"+
			"/Applications/Codex.app/Contents/MacOS/ChatGPT\x00"+
			"TOKEN=secret\x00CODEX_APP_SERVER_USE_LOCAL_DAEMON=1\x00\x00",
	)...)

	got, present, err := parseDarwinProcessEnvironmentValue(data, codexAppUseLocalDaemonEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !present || got != "1" {
		t.Fatalf("value=%q present=%v, want 1", got, present)
	}
}
