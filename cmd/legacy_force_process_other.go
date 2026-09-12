//go:build !darwin && !linux

package cmd

import (
	"context"
	"fmt"
)

func inspectLegacyProcess(int) (legacyProcessIdentity, []string, error) {
	return legacyProcessIdentity{}, nil, fmt.Errorf("当前平台不支持旧服务进程身份核验")
}
func legacyServiceUsesSystemd(runtimeState, legacyProcessIdentity) (bool, error) { return false, nil }
func stopLegacySystemdService(context.Context, legacyServiceTarget) error {
	return fmt.Errorf("当前平台不支持 systemd")
}
