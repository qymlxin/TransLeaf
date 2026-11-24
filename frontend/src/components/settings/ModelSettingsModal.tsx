import { useEffect, useState } from 'react'
import { CheckCircle2, Loader2, Plus, Trash2, Wifi, X, XCircle } from 'lucide-react'
import { api } from '../../api/client'
import type { ProviderConfig } from '../../types/document'

interface Props {
  open: boolean
  onClose: () => void
  onChanged?: () => void
}

const PRESETS = [
  { label: 'DeepSeek', base_url: 'https://api.deepseek.com/v1', model_name: 'deepseek-chat' },
  { label: 'OpenAI', base_url: 'https://api.openai.com/v1', model_name: 'gpt-4o-mini' },
  {
    label: '通义千问',
    base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1',
    model_name: 'qwen-plus',
  },
  {
    label: 'Gemini 兼容',
    base_url: 'https://generativelanguage.googleapis.com/v1beta/openai',
    model_name: 'gemini-2.5-flash',
  },
  { label: '本地 Ollama', base_url: 'http://127.0.0.1:11434/v1', model_name: 'qwen2.5:7b' },
]

export function ModelSettingsModal({ open, onClose, onChanged }: Props) {
  const [providers, setProviders] = useState<ProviderConfig[]>([])
  const [editingId, setEditingId] = useState<string | null>(null)
  const [form, setForm] = useState({
    name: 'DeepSeek',
    base_url: 'https://api.deepseek.com/v1',
    api_key: '',
    model_name: 'deepseek-chat',
    max_concurrency: 3,
    temperature: 0.3,
    is_default: true,
  })
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<{ ok: boolean; msg: string } | null>(null)
  const [saving, setSaving] = useState(false)

  const loadList = async () => {
    try {
      const list = await api.listProviders()
      setProviders(list)
    } catch {
      // 忽略
    }
  }

  useEffect(() => {
    if (open) {
      void loadList()
      setTestResult(null)
    }
  }, [open])

  if (!open) return null

  const startNew = () => {
    setEditingId(null)
    setForm({
      name: '',
      base_url: 'https://api.openai.com/v1',
      api_key: '',
      model_name: 'gpt-4o-mini',
      max_concurrency: 3,
      temperature: 0.3,
      is_default: providers.length === 0,
    })
    setTestResult(null)
  }

  const selectEdit = (p: ProviderConfig) => {
    setEditingId(p.id)
    setForm({
      name: p.name,
      base_url: p.base_url,
      api_key: p.api_key,
      model_name: p.model_name,
      max_concurrency: p.max_concurrency,
      temperature: p.temperature,
      is_default: p.is_default,
    })
    setTestResult(null)
  }

  const handleTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const res = await api.testProvider({ id: editingId ?? undefined, ...form })
      if (res.ok) {
        setTestResult({ ok: true, msg: `连接成功（响应耗时 ${res.latency_ms} ms）` })
      } else {
        setTestResult({ ok: false, msg: res.error || '连接失败' })
      }
    } catch (err) {
      setTestResult({ ok: false, msg: err instanceof Error ? err.message : '测试失败' })
    } finally {
      setTesting(false)
    }
  }

  const handleSave = async () => {
    if (!form.name.trim() || !form.model_name.trim()) {
      setTestResult({ ok: false, msg: '请填写配置名称与模型 ID' })
      return
    }
    setSaving(true)
    try {
      if (editingId) {
        await api.updateProvider(editingId, form)
      } else {
        await api.createProvider(form)
      }
      await loadList()
      onChanged?.()
      setTestResult({ ok: true, msg: '配置已保存' })
    } catch (err) {
      setTestResult({ ok: false, msg: err instanceof Error ? err.message : '保存失败' })
    } finally {
      setSaving(false)
    }
  }

  const handleDelete = async (id: string) => {
    await api.deleteProvider(id)
    if (editingId === id) startNew()
    await loadList()
    onChanged?.()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-slate-900/50 p-4 backdrop-blur-xs">
      <div className="flex max-h-[88vh] w-full max-w-3xl flex-col overflow-hidden rounded-2xl bg-white shadow-2xl">
        <div className="flex items-center justify-between border-b border-slate-200 px-6 py-4">
          <div>
            <h2 className="text-lg font-semibold text-slate-900">AI 模型服务商配置</h2>
            <p className="text-xs text-slate-500">
              兼容所有 OpenAI 协议接口，API Key 仅保存在本地 SQLite 且接口返回严格脱敏
            </p>
          </div>
          <button
            onClick={onClose}
            className="rounded-lg p-1.5 text-slate-400 hover:bg-slate-100 hover:text-slate-600"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="grid flex-1 grid-cols-1 overflow-y-auto md:grid-cols-12">
          {/* 左侧：已保存列表 */}
          <div className="border-b border-slate-200 bg-slate-50/70 p-4 md:col-span-4 md:border-r md:border-b-0">
            <div className="mb-3 flex items-center justify-between">
              <span className="text-xs font-medium tracking-wide text-slate-500 uppercase">
                已保存预设 ({providers.length})
              </span>
              <button
                onClick={startNew}
                className="inline-flex items-center gap-1 rounded-md bg-indigo-50 px-2 py-1 text-xs font-medium text-indigo-700 hover:bg-indigo-100"
              >
                <Plus className="h-3.5 w-3.5" /> 新增
              </button>
            </div>
            <div className="space-y-2">
              {providers.map((p) => (
                <div
                  key={p.id}
                  onClick={() => selectEdit(p)}
                  className={`group flex cursor-pointer items-center justify-between rounded-xl border p-3 transition ${
                    editingId === p.id
                      ? 'border-indigo-500 bg-indigo-50/60'
                      : 'border-slate-200 bg-white hover:border-slate-300'
                  }`}
                >
                  <div className="min-w-0">
                    <div className="flex items-center gap-1.5">
                      <span className="truncate text-sm font-medium text-slate-800">{p.name}</span>
                      {p.is_default && (
                        <span className="rounded bg-indigo-600 px-1.5 py-0.5 text-[10px] font-medium text-white">
                          默认
                        </span>
                      )}
                    </div>
                    <p className="truncate text-xs text-slate-500">{p.model_name}</p>
                  </div>
                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      void handleDelete(p.id)
                    }}
                    className="rounded p-1 text-slate-400 opacity-0 hover:bg-red-50 hover:text-red-600 group-hover:opacity-100"
                    title="删除"
                  >
                    <Trash2 className="h-4 w-4" />
                  </button>
                </div>
              ))}
              {providers.length === 0 && (
                <p className="py-6 text-center text-xs text-slate-400">
                  暂无模型配置，请在右侧填写并保存
                </p>
              )}
            </div>
          </div>

          {/* 右侧：表单编辑 */}
          <div className="space-y-4 p-5 md:col-span-8">
            <div>
              <span className="mb-1.5 block text-xs font-medium text-slate-500">
                快捷填充常用服务商：
              </span>
              <div className="flex flex-wrap gap-1.5">
                {PRESETS.map((pre) => (
                  <button
                    key={pre.label}
                    type="button"
                    onClick={() =>
                      setForm((f) => ({
                        ...f,
                        name: pre.label,
                        base_url: pre.base_url,
                        model_name: pre.model_name,
                      }))
                    }
                    className="rounded-md border border-slate-200 bg-slate-50 px-2.5 py-1 text-xs text-slate-700 hover:border-indigo-300 hover:bg-indigo-50 hover:text-indigo-700"
                  >
                    {pre.label}
                  </button>
                ))}
              </div>
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-700">配置名称</label>
                <input
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="例如：DeepSeek 学术翻译"
                  className="w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-700">
                  模型 ID (Model)
                </label>
                <input
                  value={form.model_name}
                  onChange={(e) => setForm({ ...form, model_name: e.target.value })}
                  placeholder="deepseek-chat / gpt-4o-mini"
                  className="w-full rounded-lg border border-slate-300 px-3 py-2 text-sm focus:border-indigo-500 focus:outline-none"
                />
              </div>
            </div>

            <div>
              <label className="mb-1 block text-xs font-medium text-slate-700">API Base URL</label>
              <input
                value={form.base_url}
                onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                placeholder="https://api.openai.com/v1"
                className="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-xs focus:border-indigo-500 focus:outline-none"
              />
            </div>

            <div>
              <label className="mb-1 block text-xs font-medium text-slate-700">
                API Key（本地 Ollama 可留空）
              </label>
              <input
                type="password"
                value={form.api_key}
                onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                placeholder="sk-..."
                className="w-full rounded-lg border border-slate-300 px-3 py-2 font-mono text-xs focus:border-indigo-500 focus:outline-none"
              />
            </div>

            <div className="grid grid-cols-2 gap-3">
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-700">
                  最大并发数 ({form.max_concurrency})
                </label>
                <input
                  type="range"
                  min={1}
                  max={8}
                  value={form.max_concurrency}
                  onChange={(e) => setForm({ ...form, max_concurrency: Number(e.target.value) })}
                  className="w-full accent-indigo-600"
                />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-700">
                  温度 Temperature ({form.temperature})
                </label>
                <input
                  type="range"
                  min={0}
                  max={1}
                  step={0.1}
                  value={form.temperature}
                  onChange={(e) => setForm({ ...form, temperature: Number(e.target.value) })}
                  className="w-full accent-indigo-600"
                />
              </div>
            </div>

            <label className="flex cursor-pointer items-center gap-2 text-sm text-slate-700">
              <input
                type="checkbox"
                checked={form.is_default}
                onChange={(e) => setForm({ ...form, is_default: e.target.checked })}
                className="h-4 w-4 rounded border-slate-300 accent-indigo-600"
              />
              设为默认翻译模型
            </label>

            {testResult && (
              <div
                className={`flex items-start gap-2 rounded-lg p-3 text-xs ${
                  testResult.ok
                    ? 'bg-emerald-50 text-emerald-800'
                    : 'bg-red-50 text-red-700'
                }`}
              >
                {testResult.ok ? (
                  <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0" />
                ) : (
                  <XCircle className="mt-0.5 h-4 w-4 shrink-0" />
                )}
                <span className="break-all">{testResult.msg}</span>
              </div>
            )}

            <div className="flex items-center justify-end gap-2 border-t border-slate-100 pt-3">
              <button
                type="button"
                disabled={testing}
                onClick={handleTest}
                className="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3.5 py-2 text-sm font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
              >
                {testing ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Wifi className="h-4 w-4" />
                )}
                测试连接
              </button>
              <button
                type="button"
                disabled={saving}
                onClick={handleSave}
                className="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white hover:bg-indigo-700 disabled:opacity-50"
              >
                {saving && <Loader2 className="h-4 w-4 animate-spin" />}
                {editingId ? '更新配置' : '保存配置'}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
