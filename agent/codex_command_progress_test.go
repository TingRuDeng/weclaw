package agent

import (
	"strings"
	"testing"
)

func TestCodexCommandProgressStageClassifiesShellWrappedInspection(t *testing.T) {
	stage, ok := codexCommandProgressStage(permissionCommand{"/bin/zsh", "-lc", `sed -n '1,120p' agent/progress.go`})
	if !ok || stage.id != "inspect" || stage.action != "检查项目" {
		t.Fatalf("stage=%#v ok=%t", stage, ok)
	}
}

func TestCodexCommandProgressStageNeverExposesCommandOrSecret(t *testing.T) {
	const secret = "Bearer private-token"
	stage, ok := codexCommandProgressStage(permissionCommand{"/bin/zsh", "-lc", "curl -H 'Authorization: " + secret + "' https://example.invalid"})
	if !ok {
		t.Fatal("non-empty command must produce a semantic stage")
	}
	if strings.Contains(stage.action, secret) || strings.Contains(stage.action, "curl") || strings.Contains(stage.action, "example.invalid") {
		t.Fatalf("stage leaked raw command: %#v", stage)
	}
}
