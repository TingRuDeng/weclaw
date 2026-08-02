package agent

import "strings"

type codexCommandStage struct {
	id     string
	action string
}

// codexCommandProgressStage 只从命令识别用户可理解的执行阶段。
// 原始命令、参数和输出可能包含路径或凭据，不得进入 ProgressEvent。
func codexCommandProgressStage(command permissionCommand) (codexCommandStage, bool) {
	raw := strings.ToLower(strings.TrimSpace(strings.Join(command, " ")))
	if raw == "" {
		return codexCommandStage{}, false
	}
	switch {
	case containsAnyCommandPhrase(raw,
		"scripts/release.sh", "gh release", "npm publish", "pnpm publish", "cargo publish"):
		return codexCommandStage{id: "release", action: "准备发布"}, true
	case containsAnyCommandPhrase(raw,
		"go test", "pytest", "python -m unittest", "python3 -m unittest", "cargo test", "swift test",
		"npm test", "npm run test", "pnpm test", "yarn test", "gradle test", "gradlew test", "xcodebuild test"):
		return codexCommandStage{id: "test", action: "运行测试"}, true
	case containsAnyCommandPhrase(raw,
		"go vet", "staticcheck", "golangci-lint", "ruff", "mypy", "typecheck", "lint",
		"git diff --check", "go mod tidy -diff", "validate_docs"):
		return codexCommandStage{id: "check", action: "运行代码检查"}, true
	case containsAnyCommandPhrase(raw,
		"go build", "cargo build", "npm run build", "pnpm build", "yarn build", "docker build",
		"gradle build", "gradlew build", "gradle assemble", "gradlew assemble", "xcodebuild"):
		return codexCommandStage{id: "build", action: "构建项目"}, true
	case containsAnyCommandPhrase(raw,
		"git status", "git diff", "git log", "git show", "rg ", "grep ", "sed ", "find ",
		"ls ", "pwd", "head ", "tail ", "jq ", "go list", "weclaw trace"):
		return codexCommandStage{id: "inspect", action: "检查项目"}, true
	default:
		return codexCommandStage{id: "tool", action: "执行工具操作"}, true
	}
}

func containsAnyCommandPhrase(command string, phrases ...string) bool {
	padded := " " + command + " "
	for _, phrase := range phrases {
		if strings.Contains(padded, phrase) {
			return true
		}
	}
	return false
}
