package server

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lakernote/beaver-agent/internal/store"
	"github.com/lakernote/beaver-agent/internal/weixin"
)

// weixinCommand 是微信控制命令的解析结果。
//
// 设计原则：只有以 "/" 开头的消息才是控制命令，其余一律当作任务交给 Agent。
// 不用关键词或语法表猜测“用户想干什么”——那样既难维护，也容易误吞正常对话。
type weixinCommand struct {
	Name     string // help / new / status / stop / projects / current_project / project
	Argument string // 仅 /project 使用：项目名
}

// weixinCommands 把 "/xxx" 映射到内部命令名。
var weixinCommands = map[string]string{
	"/help": "help", "/new": "new", "/status": "status", "/stop": "stop", "/projects": "projects",
}

var unknownCommandPattern = regexp.MustCompile(`^/[a-z]+$`)

// parseWeixinCommand 解析一条微信文本；不是控制命令时返回零值（Name 为空）。
func parseWeixinCommand(text string) weixinCommand {
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return weixinCommand{}
	}
	head := strings.ToLower(fields[0])
	if head == "/project" {
		if len(fields) == 1 {
			return weixinCommand{Name: "current_project"}
		}
		return weixinCommand{Name: "project", Argument: strings.Join(fields[1:], " ")}
	}
	// 其余命令不接受参数，避免 "/stop xxx" 之类的歧义。
	if command, ok := weixinCommands[head]; ok && len(fields) == 1 {
		return weixinCommand{Name: command}
	}
	// 形如 /xxx 的单词视为输错的命令，提示而不是当成任务；/usr/bin 之类的路径仍是普通文本。
	if unknownCommandPattern.MatchString(head) {
		return weixinCommand{Name: "unknown"}
	}
	return weixinCommand{}
}

func (manager *weixinManager) handleCommand(account store.WeixinAccount, message weixin.Message, text string) bool {
	command := parseWeixinCommand(text)
	switch command.Name {
	case "unknown":
		manager.send(account, message.FromUserID, message.ContextToken, "无法识别这个命令（或它不支持参数）。发送 /help 查看可用命令。")
		return true
	case "help":
		manager.send(account, message.FromUserID, message.ContextToken, "直接发送文字即可创建或继续任务。\n/new 开始独立的新会话\n/status 查看当前进度\n/stop 中断当前任务\n/projects 查看可用项目\n/project 项目名 设置后续新会话的源文件夹")
		return true
	case "new":
		gate := manager.accountDeliveryGate(account.ID)
		gate.Lock()
		fresh, err := manager.server.store.GetWeixinAccount(account.ID)
		if err != nil {
			gate.Unlock()
			manager.send(account, message.FromUserID, message.ContextToken, "暂时无法读取会话状态，请稍后重试。")
			return true
		}
		if session, loadErr := manager.currentSession(fresh); loadErr == nil && isActiveSessionStatus(session.Status) {
			gate.Unlock()
			manager.send(account, message.FromUserID, message.ContextToken, "当前任务仍在执行。请先发送 /stop，任务结束后再开始新会话。")
			return true
		}
		if fresh.PendingMessageID > fresh.DeliveredMessageID {
			gate.Unlock()
			manager.resumeDelivery(account.ID)
			manager.send(account, message.FromUserID, message.ContextToken, "上一条任务的结果正在回传。收到结果后请再发送 /new，避免切换会话时漏掉最终答复。")
			return true
		}
		err = manager.server.store.SetWeixinCurrentSession(account.ID, "", time.Now())
		gate.Unlock()
		if err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "切换会话失败，请稍后重试。")
			return true
		}
		manager.send(account, message.FromUserID, message.ContextToken, "已切换到新会话。下一条消息会创建一个独立任务。")
		return true
	case "status":
		session, err := manager.currentSession(account)
		if errors.Is(err, sql.ErrNoRows) || strings.TrimSpace(account.CurrentSessionID) == "" {
			manager.send(account, message.FromUserID, message.ContextToken, "当前没有会话。直接发送任务即可开始。")
		} else if err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "暂时无法读取任务状态，请稍后重试。")
		} else {
			progress := manager.server.tasks.progress(session.ID)
			if progress == "" {
				progress = weixinSessionStatus(session.Status)
			}
			manager.send(account, message.FromUserID, message.ContextToken, fmt.Sprintf("💬 当前会话：%s\n状态：%s\n🧭 进度：%s\n⚙️ 运行时：%s", session.Title, weixinSessionStatus(session.Status), progress, weixinRuntimeLabel(session.Runtime)))
		}
		return true
	case "stop":
		session, err := manager.currentSession(account)
		if err != nil || !isActiveSessionStatus(session.Status) {
			manager.send(account, message.FromUserID, message.ContextToken, "当前没有正在执行的任务。发送 /new 可开始独立任务。")
			return true
		}
		changed, cancelErr := manager.server.store.CancelSession(session.ID, time.Now())
		if cancelErr != nil || !changed {
			manager.send(account, message.FromUserID, message.ContextToken, "任务已经结束，未执行停止。")
			return true
		}
		manager.server.tasks.cancel(session.ID)
		manager.send(account, message.FromUserID, message.ContextToken, "⏹️ 已停止当前任务。")
		return true
	case "projects":
		projects, err := manager.server.projects()
		if err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "暂时无法读取项目列表，请稍后重试。")
			return true
		}
		lines := []string{"可用项目："}
		for _, project := range projects {
			mark := ""
			if project.ID == account.ProjectID || (account.ProjectID == "" && project.Default) {
				mark = "（当前）"
			}
			lines = append(lines, "- "+project.Name+mark)
		}
		lines = append(lines, "发送 /project 项目名可切换。")
		manager.send(account, message.FromUserID, message.ContextToken, strings.Join(lines, "\n"))
		return true
	case "current_project":
		project, err := manager.weixinProject(account)
		if err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "暂时无法读取当前项目，请稍后重试。")
		} else {
			manager.send(account, message.FromUserID, message.ContextToken, fmt.Sprintf("当前项目：%s\n主源文件夹：%s\n共 %d 个源文件夹。", project.Name, project.Directories[0], len(project.Directories)))
		}
		return true
	case "project":
		projects, err := manager.server.projects()
		if err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "暂时无法切换项目，请稍后重试。")
			return true
		}
		var selected *store.Project
		for index := range projects {
			if strings.EqualFold(strings.TrimSpace(projects[index].Name), command.Argument) {
				selected = &projects[index]
				break
			}
		}
		if selected == nil {
			manager.send(account, message.FromUserID, message.ContextToken, "没有找到项目“"+command.Argument+"”。发送 /projects查看可用项目。")
			return true
		}
		if err := manager.server.store.SetWeixinProject(account.ID, selected.ID, time.Now()); err != nil {
			manager.send(account, message.FromUserID, message.ContextToken, "项目切换失败，请稍后重试。")
			return true
		}
		manager.send(account, message.FromUserID, message.ContextToken, "已选择项目“"+selected.Name+"”。当前会话不变；发送 /new后，新任务会使用这个项目的源文件夹。")
		return true
	default:
		return false
	}
}
