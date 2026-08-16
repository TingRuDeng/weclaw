//go:build !darwin

package agent

import "context"

func configureSystemCodexAppDaemonReuse(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}

func inspectSystemCodexAppDaemonReuse(context.Context) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}
