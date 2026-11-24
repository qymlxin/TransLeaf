import { useState } from 'react'
import {
  AlignCenter,
  AlignLeft,
  AlignRight,
  Check,
  CheckCircle2,
  Loader2,
  PanelRightClose,
  Sparkles,
} from 'lucide-react'
import { api } from '../../api/client'
import { useWorkspaceStore } from '../../store/useWorkspaceStore'
import type { TranslationBlock } from '../../types/document'

const AI_INSTRUCTIONS = [
  { label: '更专业流畅', value: '请重新翻译并润色，使表达更加专业、自然、符合中文习惯。' },
  { label: '更学术严谨', value: '请改写为严谨规范的学术论文中文表达，保留核心专业术语。' },
  { label: '精简适配版面', value: '请在不丢失核心语义的前提下精简译文字数约 25%，以便适配原有 PDF 排版宽度。' },
  { label: '直译校对', value: '请严格忠实于原文逐句直译，不随意增删细节。' },
]

interface Props {
  open: boolean
  onClose: () => void
}

export function BlockInspector({ open, onClose }: Props) {
  const { doc, selectedBlockId, updateBlockLocal } = useWorkspaceStore()
  const [instruction, setInstruction] = useState(AI_INSTRUCTIONS[0].value)
  const [aiLoading, setAiLoading] = useState(false)
  const [suggestion, setSuggestion] = useState<string | null>(null)
  const [aiError, setAiError] = useState<string | null>(null)

  if (!open) return null

  let selected: TranslationBlock | null = null
  if (doc && selectedBlockId) {
    for (const p of doc.pages) {
      const f = p.blocks.find((b) => b.id === selectedBlockId)
      if (f) {
        selected = f
        break
      }
    }
  }

  if (!doc || !selected) {
    return (
      <aside className="flex w-80 shrink-0 flex-col border-l border-slate-200/90 bg-white">
        <div className="flex items-center justify-between border-b border-slate-100 px-4 py-3">
          <span className="text-xs font-semibold text-slate-700">段落检查器</span>
          <button
            onClick={onClose}
            className="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600"
            title="收起检查器"
          >
            <PanelRightClose className="h-4 w-4" />
          </button>
        </div>
        <div className="flex flex-1 flex-col items-center justify-center px-6 text-center text-xs text-slate-400">
          <p className="font-medium text-slate-600">未选中文本块</p>
          <p className="mt-1.5 leading-relaxed">
            点击左侧或右侧画布中的任意段落，即可在此微调译文、字号、对齐方式或调用 AI 局部润色。
          </p>
        </div>
      </aside>
    )
  }

  const blk = selected

  const handleAIRetranslate = async () => {
    setAiLoading(true)
    setAiError(null)
    setSuggestion(null)
    try {
      const res = await api.retranslateBlock(doc.id, blk.id, {
        instruction,
        target_lang: doc.target_lang,
      })
      setSuggestion(res.translated_text)
    } catch (err) {
      setAiError(err instanceof Error ? err.message : 'AI 局部润色失败')
    } finally {
      setAiLoading(false)
    }
  }

  return (
    <aside className="flex w-80 shrink-0 flex-col overflow-y-auto border-l border-slate-200/90 bg-white">
      {/* 头部信息 */}
      <div className="flex items-center justify-between border-b border-slate-100 px-4 py-3">
        <div className="min-w-0">
          <span className="font-mono text-xs font-semibold text-slate-800">#{blk.id}</span>
          <span className="ml-2 rounded-md bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600">
            P{blk.page_index + 1} · {blk.block_type}
          </span>
        </div>
        <div className="flex items-center gap-1">
          <button
            onClick={() =>
              updateBlockLocal(
                blk.id,
                { status: blk.status === 'reviewed' ? 'edited' : 'reviewed' },
                true,
              )
            }
            className={`inline-flex items-center gap-1 rounded-lg px-2 py-1 text-xs font-medium transition ${
              blk.status === 'reviewed'
                ? 'bg-emerald-100 text-emerald-700'
                : 'bg-slate-100 text-slate-600 hover:bg-emerald-50 hover:text-emerald-700'
            }`}
          >
            <CheckCircle2 className="h-3.5 w-3.5" />
            {blk.status === 'reviewed' ? '已审校' : '审校'}
          </button>
          <button
            onClick={onClose}
            className="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600"
            title="收起检查器"
          >
            <PanelRightClose className="h-4 w-4" />
          </button>
        </div>
      </div>

      <div className="space-y-4 p-4">
        {/* 原文只读 */}
        <div>
          <label className="mb-1.5 block text-xs font-medium text-slate-500">原文</label>
          <div className="max-h-32 overflow-y-auto rounded-xl border border-slate-200/80 bg-slate-50 p-3 text-xs leading-relaxed text-slate-700">
            {blk.original_text}
          </div>
        </div>

        {/* 译文实时编辑 */}
        <div>
          <label className="mb-1.5 block text-xs font-medium text-slate-700">
            译文（自动防抖保存）
          </label>
          <textarea
            rows={5}
            value={blk.translated_text}
            disabled={!blk.translatable}
            onChange={(e) => updateBlockLocal(blk.id, { translated_text: e.target.value })}
            placeholder={blk.translatable ? '输入或修改译文...' : '该块已设置为跳过翻译'}
            className="w-full rounded-xl border border-slate-200 p-3 text-sm leading-relaxed text-slate-900 shadow-2xs transition focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/15 focus:outline-none disabled:bg-slate-100 disabled:text-slate-400"
          />
        </div>

        {/* AI 局部重译 / 润色 */}
        {blk.translatable && (
          <div className="rounded-2xl border border-indigo-100 bg-indigo-50/40 p-3.5">
            <div className="mb-2 flex items-center gap-1.5 text-xs font-semibold text-indigo-800">
              <Sparkles className="h-3.5 w-3.5" />
              AI 局部润色 / 重译
            </div>
            <div className="mb-2 flex flex-wrap gap-1">
              {AI_INSTRUCTIONS.map((item) => (
                <button
                  key={item.label}
                  type="button"
                  onClick={() => setInstruction(item.value)}
                  className={`rounded-md px-2 py-0.5 text-[11px] transition ${
                    instruction === item.value
                      ? 'bg-indigo-600 text-white'
                      : 'bg-white text-slate-600 ring-1 ring-slate-200 hover:bg-indigo-50'
                  }`}
                >
                  {item.label}
                </button>
              ))}
            </div>
            <input
              value={instruction}
              onChange={(e) => setInstruction(e.target.value)}
              placeholder="输入自定义润色指令..."
              className="mb-2.5 w-full rounded-lg border border-indigo-200 bg-white px-2.5 py-1.5 text-xs focus:border-indigo-500 focus:outline-none"
            />
            <button
              type="button"
              disabled={aiLoading}
              onClick={handleAIRetranslate}
              className="inline-flex w-full items-center justify-center gap-1.5 rounded-xl bg-indigo-600 py-2 text-xs font-medium text-white shadow-2xs transition hover:bg-indigo-700 disabled:opacity-50"
            >
              {aiLoading ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Sparkles className="h-3.5 w-3.5" />
              )}
              生成 AI 建议
            </button>

            {aiError && <p className="mt-2 text-xs text-red-600">{aiError}</p>}

            {suggestion && (
              <div className="mt-2.5 rounded-xl border border-indigo-200 bg-white p-3">
                <p className="text-xs leading-relaxed text-slate-800">{suggestion}</p>
                <div className="mt-2 flex justify-end gap-2">
                  <button
                    onClick={() => setSuggestion(null)}
                    className="rounded-lg px-2 py-1 text-xs text-slate-500 hover:bg-slate-100"
                  >
                    放弃
                  </button>
                  <button
                    onClick={() => {
                      updateBlockLocal(blk.id, { translated_text: suggestion }, true)
                      setSuggestion(null)
                    }}
                    className="inline-flex items-center gap-1 rounded-lg bg-emerald-600 px-2.5 py-1 text-xs font-medium text-white hover:bg-emerald-700"
                  >
                    <Check className="h-3 w-3" /> 采纳
                  </button>
                </div>
              </div>
            )}
          </div>
        )}

        {/* 排版微调 */}
        <div className="space-y-3 border-t border-slate-100 pt-3">
          <p className="text-xs font-semibold text-slate-700">排版微调（导出 PDF 严格还原）</p>

          <div className="grid grid-cols-2 gap-2.5">
            <div>
              <label className="mb-1 block text-[11px] text-slate-500">译文字号 (pt)</label>
              <input
                type="number"
                step={0.5}
                min={6}
                max={48}
                value={blk.style.translated_font_size || blk.style.font_size}
                onChange={(e) =>
                  updateBlockLocal(blk.id, {
                    style: {
                      ...blk.style,
                      translated_font_size: Number(e.target.value) || blk.style.font_size,
                    },
                  })
                }
                className="w-full rounded-lg border border-slate-200 px-2.5 py-1.5 text-xs focus:border-indigo-500 focus:outline-none"
              />
            </div>
            <div>
              <label className="mb-1 block text-[11px] text-slate-500">行高倍数</label>
              <input
                type="number"
                step={0.05}
                min={0.9}
                max={2.5}
                value={blk.style.line_height || 1.25}
                onChange={(e) =>
                  updateBlockLocal(blk.id, {
                    style: {
                      ...blk.style,
                      line_height: Number(e.target.value) || 1.25,
                    },
                  })
                }
                className="w-full rounded-lg border border-slate-200 px-2.5 py-1.5 text-xs focus:border-indigo-500 focus:outline-none"
              />
            </div>
          </div>

          <div className="flex items-center justify-between">
            <span className="text-[11px] text-slate-500">对齐方式</span>
            <div className="inline-flex rounded-lg border border-slate-200 bg-slate-50 p-0.5">
              {(
                [
                  { k: 'left', icon: AlignLeft },
                  { k: 'center', icon: AlignCenter },
                  { k: 'right', icon: AlignRight },
                ] as const
              ).map(({ k, icon: Icon }) => (
                <button
                  key={k}
                  onClick={() =>
                    updateBlockLocal(blk.id, { style: { ...blk.style, align: k } }, true)
                  }
                  className={`rounded-md p-1.5 transition ${
                    blk.style.align === k
                      ? 'bg-white text-indigo-700 shadow-2xs'
                      : 'text-slate-500 hover:text-slate-800'
                  }`}
                >
                  <Icon className="h-3.5 w-3.5" />
                </button>
              ))}
            </div>
          </div>

          <div className="flex items-center justify-between">
            <span className="text-[11px] text-slate-500">文字颜色</span>
            <input
              type="color"
              value={blk.style.color || '#000000'}
              onChange={(e) =>
                updateBlockLocal(blk.id, { style: { ...blk.style, color: e.target.value } })
              }
              className="h-7 w-12 cursor-pointer rounded-md border border-slate-200"
            />
          </div>

          <label className="flex cursor-pointer items-center justify-between rounded-xl border border-slate-200 px-3 py-2 text-xs text-slate-700 transition hover:bg-slate-50">
            <span>参与翻译（关闭则保留原文）</span>
            <input
              type="checkbox"
              checked={blk.translatable}
              onChange={(e) =>
                updateBlockLocal(blk.id, { translatable: e.target.checked }, true)
              }
              className="h-4 w-4 accent-indigo-600"
            />
          </label>
        </div>
      </div>
    </aside>
  )
}
