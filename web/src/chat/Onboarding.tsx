import type { Bootstrap } from '../types'

type Props = { data: Bootstrap; reason: string; onOpenModelSettings: (runtime: 'beaver' | 'codex') => void }

// 首次使用时没有可用模型，给出三条明确的起步路径，而不是只显示一条“配置不完整”。
export function Onboarding({ data, reason, onOpenModelSettings }: Props) {
  const codexReady = data.codex.installed && data.codex.appServerAvailable && (data.codex.authenticated || data.codexConfig.configured)
  const options = [
    { key: 'ollama', title: '本地 Ollama', badge: data.ollama.running ? '已检测到' : '免费 · 离线', ready: data.ollama.running, text: '在本机运行开源模型，数据不出门。', action: '配置 Ollama', runtime: 'beaver' as const },
    { key: 'api', title: 'API Key', badge: 'OpenAI 兼容', ready: false, text: 'OpenRouter、Gemini、Groq、DeepSeek 等，选模板填入 Key 即可。', action: '填写 API Key', runtime: 'beaver' as const },
    { key: 'codex', title: 'Codex CLI', badge: codexReady ? '已就绪' : data.codex.installed ? '需登录' : '未安装', ready: codexReady, text: '更强的代码能力，使用你的 Codex 账号或自定义网关。', action: codexReady ? '查看配置' : '配置 Codex', runtime: 'codex' as const },
  ]
  return (
    <section className="onboarding" aria-labelledby="onboarding-title">
      <div className="onboarding-head"><span className="onboarding-step">第 1 步</span><div><h2 id="onboarding-title">先连接一个模型</h2><p>{reason}</p></div></div>
      <div className="onboarding-options">
        {options.map((option) => (
          <button key={option.key} type="button" className={`onboarding-option ${option.ready ? 'ready' : ''}`} onClick={() => onOpenModelSettings(option.runtime)}>
            <span className="onboarding-badge">{option.badge}</span>
            <strong>{option.title}</strong>
            <small>{option.text}</small>
            <em>{option.action} →</em>
          </button>
        ))}
      </div>
    </section>
  )
}
