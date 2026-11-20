package codexruntime

import "github.com/lakernote/beaver-agent/internal/permissions"

// codexSandboxMode 返回 thread/start 使用的字符串形式沙箱模式；
// turn/start 使用下面的对象形式。协议细节集中在本文件，不泄漏到 server 和设置。
func codexSandboxMode(policy permissions.Policy) string {
	switch policy.Normalize().Mode {
	case permissions.ModeReadOnly:
		return "read-only"
	case permissions.ModeWorkspaceWrite:
		return "workspace-write"
	case permissions.ModeCustom:
		return "workspace-write"
	default:
		return "danger-full-access"
	}
}

func codexSandboxPolicy(policy permissions.Policy, defaults []string) map[string]any {
	policy = policy.Normalize()
	switch policy.Mode {
	case permissions.ModeReadOnly:
		return map[string]any{"type": "readOnly"}
	case permissions.ModeWorkspaceWrite:
		return map[string]any{"type": "workspaceWrite", "writableRoots": defaults, "networkAccess": policy.NetworkAccess}
	case permissions.ModeCustom:
		return map[string]any{"type": "workspaceWrite", "writableRoots": policy.EffectiveWritableRoots(defaults), "networkAccess": policy.NetworkAccess}
	default:
		return map[string]any{"type": "dangerFullAccess"}
	}
}
