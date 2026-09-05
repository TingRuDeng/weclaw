//go:build !darwin

package agent

import "context"

func configureSystemCodexAppDaemonReuse(context.Context, bool, string) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}

func configureSystemCodexAppDaemonReuseWithExpected(
	context.Context,
	bool,
	string,
	codexAppDaemonEnvironment,
) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}

func inspectSystemCodexAppDaemonReuse(context.Context) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}

func inspectSystemCodexAppDaemonReuseWithExpected(
	context.Context,
	*codexAppDaemonEnvironment,
) (codexAppDaemonReuseResult, error) {
	return codexAppDaemonReuseResult{}, nil
}
