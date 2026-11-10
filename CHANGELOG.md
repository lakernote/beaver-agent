# 更新日志

## 未发布

- **密码与登录**：系统生成的初始/重置密码在修改前会显示顶部提醒，并在账户安全页自动展开改密表单；登录页支持显示/隐藏密码并折叠首次使用说明；同一来源连续 5 次登录失败后锁定 10 分钟；随机密码生成避免取模偏差；设置页去掉重复的“新会话默认”信息。
- **清理与修复**：移除 7 个无调用的 Go 函数与约 240 条未使用的 CSS 规则（样式减少约 22 KB）；定时任务/Skills 弹窗支持 Esc 关闭；手机端去掉多余的底部留白并改用 dvh；用量页与定时任务空状态样式修正。
- **品牌视觉**：全站由灰绿改为 Beaver 蓝（海军蓝 + 青色点缀）：侧边栏渐变、会话选中态、用户气泡、输入框、发送按钮、代码块、登录页；新增 32px favicon 与 apple-touch-icon；README 截图按新界面重拍。
- Web UI 可读性：原先大量 7–10px 的辅助文字整体上调约 1–2px（共约 400 处），中文标签不再使用等宽字体；手机端首次引导时收起重复的配置提示条。
- **文档**：README 增加“个人电脑 / 服务器”部署对照、两种引擎对比，并说明微信无需公网 IP。
- Web UI：手机端逐页检查设置、Skills、工具与 MCP、微信、用量页，未发现页面级横向溢出；手机底部导航不再显示通知按钮。
- **首次引导**：没有可用模型时，首页展示 Ollama / API Key / Codex CLI 三条起步路径（含检测状态），不再只显示一条“配置不完整”。
- **任务通知**：任务完成或失败且页面在后台时弹出浏览器通知；标签页标题显示运行中/已完成数量；后台（微信、定时）任务运行期间自动低频刷新。
- **安全**：首次启动不再使用 `admin/admin`，改为生成随机初始密码并在日志中打印一次；新增 `beaver -reset-password` 重置管理员密码；登录页提示同步更新。
- **更名**：项目更名为 Beaver，二进制、环境变量（`BEAVER_*`）、数据目录（`~/.beaver/`）、Go 模块与 Runtime ID 一并更名，不保留旧名兼容。
- 文档与开源治理：新增英文 README、文档索引、行为准则、docker-compose、Dependabot、Issue 模板配置与 `.editorconfig`，修复 README 中的占位符与内网地址。
- 品牌：新 Logo（任务轨道环 + 扫描扇区 + 友好 Agent），新增 README 用 512px 图标，侧边栏加入“自托管 Agent 工作台”副标题。
- Web UI：补充全局键盘焦点样式、`prefers-reduced-motion` 支持与页面元信息；新增 ErrorBoundary，渲染异常时提示刷新而非白屏；清理 32 条重复 CSS 规则；修复手机端新会话输入区权限/模型被挤成整行的问题。
- 模型默认值与校验范围集中管理，新数据库不再猜测用户已经下载某个 Ollama 模型。
- 增加 OpenRouter、Gemini 和 Groq 免费额度配置模板，并支持 `BEAVER_OLLAMA_URL`。
- 移除整轮 Agent 的固定十分钟超时；模型与工具继续使用各自超时和主动取消。
- 切换模型服务时不再沿用旧 API Key，避免跨 Provider 误发密钥。
- 增加模型 Agent 能力测试，真实验证原生 Function Calling 和工具结果回传。

## 0.1.0

- 重构为 EasyAgent（现 Beaver）：一个核心 Agent，不使用 Graph、多 Agent 或固定业务状态机。
- 支持 OpenAI Chat Completions、OpenAI Responses 和兼容模型服务。
- 支持 SQLite 多轮会话、消息历史和完整 Agent Trace。
- 将 System Prompt、内置 Skills、内置 Tools、MCP 预设拆成独立 Go packages。
- 支持在页面编辑和启停 Skills，添加、认证、测试和启停 MCP。
- 修复聊天 Markdown 渲染，增加高频内置 Skills 和 Playwright MCP 的依赖检测、自动安装、连接测试与启用流程。
- 新增对话式 Web UI、Token/缓存率/耗时汇总和 JSON 输入输出查看。
- 新增 Provider 缓存 Token 兼容、上下文账本和可配置的自动会话压缩；原始消息完整保留，压缩调用进入 Trace。
- 参考 Pi/Codex 将基础 System Prompt 精简分层，并为上下文检查点使用独立 Prompt。
- Skill 改为页面内创建；MCP 启用前强制完成认证和连接校验，并通过 `search_mcp_tools` 按任务语义加载少量远端工具。
- 新增任务排队状态和运行中取消，取消信号会传播到模型与工具调用。
- 新增无需外部解释器的 `calculate` 和可审计、可取消、带超时与输出截断的 `shell`。
- MCP 元数据增加用途描述，GitHub 预设限制为常用 Toolsets；Playwright 预设在安装前检查当前运行时实际要求的 Node.js 20+。
- Chat Completions 新增 SSE 流式读取和页面增量展示，并兼容忽略流式参数的普通 JSON Provider。
- 模型请求超时改为页面可配置，默认 300 秒；Ollama 禁用推理改用标准 `reasoning_effort=none`。
- Ollama 上下文窗口读取当前 `/api/ps` 的真实运行值，不再把理论上限用于压缩阈值。
- 失败和取消的模型调用也会保存次数、耗时与 Trace；修复 Trace 主键丢失及 Token 未上报被误显示为 0。
- 新增 `api-design` 高频 Skill，要求合法 JSON、完整接口契约和可运行验证。
- 模型始终看到稳定的常驻工具集合并通过原生 `tool_choice=auto` 自主选择；空流会记录原调用后关闭流式重试一次。
- “继续 / 接着做”类短指令只在模型输入中补充续写语义，SQLite 与页面仍保留用户原话。
- 删除旧的项目、事故、归因、修复、发布和兼容代码。
