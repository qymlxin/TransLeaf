import { useState } from 'react'
import { CheckCircle2, Search } from 'lucide-react'
import { useWorkspaceStore } from '../../store/useWorkspaceStore'
import type { BlockStatus } from '../../types/document'

const STATUS_OPTIONS: Array<{ value: string; label: string }> = [
  { value: 'all', label: '全部状态' },
  { value: 'pending', label: '待翻译' },
  { value: 'translated', label: '已翻译' },
  { value: 'edited', label: '已编辑' },
  { value: 'reviewed', label: '已审校' },
  { value: 'failed', label: '失败' },
  { value: 'skipped', label: '已跳过' },
]

// CAT 计算机辅助翻译段落对照校对列表视图
export function SegmentListView() {
  const { doc, selectedBlockId, selectBlock, updateBlockLocal } = useWorkspaceStore()
  const [keyword, setKeyword] = useState('')
  const [statusFilter, setStatusFilter] = useState<string>('all')

  if (!doc) return null

  const allBlocks = doc.pages.flatMap((p) => p.blocks)
  const filtered = allBlocks.filter((b) => {
    if (statusFilter !== 'all' && b.status !== statusFilter) return false
    if (keyword.trim()) {
      const q = keyword.trim().toLowerCase()
      return (
        b.original_text.toLowerCase().includes(q) ||
        b.translated_text.toLowerCase().includes(q)
      )
    }
    return true
  })

  return (
    <div className="flex flex-1 flex-col overflow-hidden bg-slate-50">
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-200 bg-white px-5 py-3">
        <div className="relative w-72">
          <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-slate-400" />
          <input
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            placeholder="搜索原文或译文关键词..."
            className="w-full rounded-lg border border-slate-200 py-1.5 pr-3 pl-9 text-sm focus:border-indigo-500 focus:outline-none"
          />
        </div>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          className="rounded-lg border border-slate-200 bg-white px-3 py-1.5 text-sm text-slate-700 focus:border-indigo-500 focus:outline-none"
        >
          {STATUS_OPTIONS.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))}
        </select>
        <span className="ml-auto text-xs text-slate-500">
          共显示 {filtered.length} / {allBlocks.length} 个文本块
        </span>
      </div>

      <div className="flex-1 space-y-3 overflow-y-auto p-5">
        {filtered.map((blk) => {
          const isSelected = selectedBlockId === blk.id
          return (
            <div
              key={blk.id}
              onClick={() => selectBlock(blk.id)}
              className={`grid grid-cols-1 gap-4 rounded-xl border bg-white p-4 transition md:grid-cols-2 ${
                isSelected ? 'border-indigo-500 ring-1 ring-indigo-500' : 'border-slate-200'
              }`}
            >
              <div>
                <div className="mb-1.5 flex items-center gap-2 text-[11px] text-slate-400">
                  <span className="font-mono font-medium text-slate-600">#{blk.id}</span>
                  <span>第 {blk.page_index + 1} 页</span>
                  <StatusPill status={blk.status} />
                </div>
                <p className="text-sm leading-relaxed text-slate-700">{blk.original_text}</p>
              </div>

              <div className="flex flex-col">
                <div className="mb-1.5 flex items-center justify-between">
                  <span className="text-[11px] font-medium text-slate-400">译文</span>
                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      updateBlockLocal(
                        blk.id,
                        { status: blk.status === 'reviewed' ? 'edited' : 'reviewed' },
                        true,
                      )
                    }}
                    className={`inline-flex items-center gap-1 rounded px-2 py-0.5 text-[11px] font-medium ${
                      blk.status === 'reviewed'
                        ? 'bg-emerald-100 text-emerald-700'
                        : 'text-slate-500 hover:bg-emerald-50 hover:text-emerald-700'
                    }`}
                  >
                    <CheckCircle2 className="h-3.5 w-3.5" />
                    {blk.status === 'reviewed' ? '已审校' : '标记审校'}
                  </button>
                </div>
                <textarea
                  rows={3}
                  disabled={!blk.translatable}
                  value={blk.translated_text}
                  onChange={(e) => updateBlockLocal(blk.id, { translated_text: e.target.value })}
                  placeholder={blk.translatable ? '待翻译或手动输入...' : '已跳过翻译'}
                  className="w-full flex-1 rounded-lg border border-slate-200 p-2 text-sm leading-relaxed text-slate-900 focus:border-indigo-500 focus:outline-none disabled:bg-slate-50"
                />
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function StatusPill({ status }: { status: BlockStatus }) {
  const map: Record<BlockStatus, string> = {
    skipped: 'bg-slate-100 text-slate-500',
    pending: 'bg-slate-100 text-slate-600',
    translating: 'bg-indigo-100 text-indigo-700',
    translated: 'bg-sky-100 text-sky-700',
    edited: 'bg-amber-100 text-amber-700',
    reviewed: 'bg-emerald-100 text-emerald-700',
    failed: 'bg-red-100 text-red-700',
  }
  return <span className={`rounded px-1.5 py-0.5 text-[10px] font-medium ${map[status]}`}>{status}</span>
}
