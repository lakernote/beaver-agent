package server

import "strings"

// weixinIntent 是微信控制命令的解析结果。
//
// 设计原则：只有以 "/" 开头的消息才是控制命令，其余一律当作任务交给 Agent。
// 不用关键词或语法表猜测“用户想干什么”——那样既难维护，也容易误吞正常对话。
type weixinIntent struct {
	Command  string // help / new / status / stop / projects / current_project / project
	Argument string // 仅 /project 使用：项目名
}

// weixinCommands 把 "/xxx" 映射到内部命令名。
var weixinCommands = map[string]string{
	"/help": "help", "/new": "new", "/status": "status", "/stop": "stop", "/projects": "projects",
}

// parseWeixinCommand 解析一条微信文本；不是控制命令时返回零值（Command 为空）。
func parseWeixinCommand(text string) weixinIntent {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return weixinIntent{}
	}
	head := strings.ToLower(fields[0])
	if head == "/project" {
		if len(fields) == 1 {
			return weixinIntent{Command: "current_project"}
		}
		return weixinIntent{Command: "project", Argument: strings.Join(fields[1:], " ")}
	}
	// 其余命令不接受参数，避免 "/stop xxx" 之类的歧义。
	if command, ok := weixinCommands[head]; ok && len(fields) == 1 {
		return weixinIntent{Command: command}
	}
	return weixinIntent{}
}
