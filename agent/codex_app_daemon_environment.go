package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/codexauth"
)

const codexAppSQLiteHomeEnv = "CODEX_SQLITE_HOME"

// codexAppDaemonEnvironment describes the paths that must resolve to the same
// state namespace for WeClaw's daemon and a subsequently launched Codex App.
// An empty CodexSQLiteHome means that Codex uses CodexHome as its default.
type codexAppDaemonEnvironment struct {
	CodexHome        string
	CodexSQLiteHome  string
	DefaultCodexHome string
}

func (e codexAppDaemonEnvironment) effectiveSQLiteHome() string {
	if e.CodexSQLiteHome != "" {
		return e.CodexSQLiteHome
	}
	return e.CodexHome
}

func (e codexAppDaemonEnvironment) validate() error {
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "CODEX_HOME", value: e.CodexHome},
		{name: "default Codex home", value: e.DefaultCodexHome},
	} {
		if item.value == "" {
			return fmt.Errorf("%s is empty", item.name)
		}
		if !filepath.IsAbs(item.value) || filepath.Clean(item.value) != item.value {
			return fmt.Errorf("%s must be an absolute clean path: %s", item.name, item.value)
		}
	}
	if e.CodexSQLiteHome != "" &&
		(!filepath.IsAbs(e.CodexSQLiteHome) || filepath.Clean(e.CodexSQLiteHome) != e.CodexSQLiteHome) {
		return fmt.Errorf("%s must be an absolute clean path: %s", codexAppSQLiteHomeEnv, e.CodexSQLiteHome)
	}
	return nil
}

// resolveCodexAppDaemonEnvironment resolves the same process environment used
// by the official daemon before it is copied into launchd for future App
// launches. The SQLite override follows mergeEnv's map-over-process precedence.
func (a *ACPAgent) resolveCodexAppDaemonEnvironment() (codexAppDaemonEnvironment, error) {
	codexHome, err := codexauth.ResolveCodexHome(a.env, a.runAs.User)
	if err != nil {
		return codexAppDaemonEnvironment{}, fmt.Errorf("resolve CODEX_HOME: %w", err)
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return codexAppDaemonEnvironment{}, fmt.Errorf("resolve user home: %w", err)
	}
	defaultHome, err := normalizeCodexAbsoluteEnvironmentPath(
		"default CODEX_HOME", filepath.Join(userHome, ".codex"),
	)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	sqliteValue := codexAgentEnvironmentValue(a.env, codexAppSQLiteHomeEnv)
	sqliteHome, err := normalizeOptionalCodexEnvironmentPath(codexAppSQLiteHomeEnv, sqliteValue)
	if err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	environment := codexAppDaemonEnvironment{
		CodexHome:        filepath.Clean(codexHome),
		CodexSQLiteHome:  sqliteHome,
		DefaultCodexHome: defaultHome,
	}
	if err := environment.validate(); err != nil {
		return codexAppDaemonEnvironment{}, err
	}
	return environment, nil
}

func codexAgentEnvironmentValue(environment map[string]string, name string) string {
	if value, ok := environment[name]; ok {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(name))
}

func normalizeOptionalCodexEnvironmentPath(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	return normalizeCodexAbsoluteEnvironmentPath(name, value)
}

func normalizeCodexAbsoluteEnvironmentPath(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is empty", name)
	}
	if !filepath.IsAbs(value) {
		return "", fmt.Errorf("%s must be absolute: %s", name, value)
	}
	return filepath.Clean(value), nil
}
