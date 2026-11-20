// Package permissions 定义与 Runtime 无关的权限契约。
//
// Beaver 和 Codex 的执行引擎不同，但用户不应该学两套权限词汇。
// 各自的适配器负责把这份策略翻译成原生的工具/沙箱控制。
package permissions

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Mode string

const (
	ModeReadOnly       Mode = "read_only"
	ModeWorkspaceWrite Mode = "workspace_write"
	ModeFullAccess     Mode = "full_access"
	ModeCustom         Mode = "custom"
)

type ApprovalPolicy string

const (
	ApprovalOnRequest ApprovalPolicy = "on-request"
	ApprovalNever     ApprovalPolicy = "never"
)

// Policy 保存在 RuntimeSettings 中，由两个 Runtime 共用。
// WritableRoots 是绝对路径；为空表示当前会话工作区及其项目源目录。
type Policy struct {
	Mode          Mode           `json:"mode"`
	Approval      ApprovalPolicy `json:"approval"`
	WritableRoots []string       `json:"writableRoots,omitempty"`
	NetworkAccess bool           `json:"networkAccess"`
}

func Default() Policy {
	return Policy{Mode: ModeFullAccess, Approval: ApprovalNever, NetworkAccess: true}
}

func (policy Policy) Normalize() Policy {
	if policy.Mode == "" {
		policy.Mode = ModeFullAccess
	}
	if policy.Approval == "" {
		if policy.Mode == ModeFullAccess {
			policy.Approval = ApprovalNever
		} else {
			policy.Approval = ApprovalOnRequest
		}
	}
	roots := make([]string, 0, len(policy.WritableRoots))
	seen := make(map[string]struct{}, len(policy.WritableRoots))
	for _, root := range policy.WritableRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if absolute, err := filepath.Abs(root); err == nil {
			root = filepath.Clean(absolute)
		}
		if _, exists := seen[root]; exists {
			continue
		}
		seen[root] = struct{}{}
		roots = append(roots, root)
	}
	policy.WritableRoots = roots
	return policy
}

func (policy Policy) Validate() error {
	policy = policy.Normalize()
	switch policy.Mode {
	case ModeReadOnly, ModeWorkspaceWrite, ModeFullAccess, ModeCustom:
	default:
		return fmt.Errorf("不支持的权限模式: %q", policy.Mode)
	}
	switch policy.Approval {
	case ApprovalOnRequest, ApprovalNever:
	default:
		return fmt.Errorf("不支持的审批策略: %q", policy.Approval)
	}
	if policy.Mode == ModeReadOnly && len(policy.WritableRoots) > 0 {
		return fmt.Errorf("只读模式不能配置可写目录")
	}
	return nil
}

func (policy Policy) IsReadOnly() bool { return policy.Normalize().Mode == ModeReadOnly }

func (policy Policy) IsFullAccess() bool { return policy.Normalize().Mode == ModeFullAccess }

// EffectiveWritableRoots 返回 Beaver 适配器对某个会话应强制执行的可写根目录。
// 调用方传入由工作区派生的默认值，使全局策略对每个会话的 worktree 依然有效。
func (policy Policy) EffectiveWritableRoots(defaults []string) []string {
	policy = policy.Normalize()
	if len(policy.WritableRoots) > 0 {
		return append([]string(nil), policy.WritableRoots...)
	}
	return append([]string(nil), defaults...)
}
