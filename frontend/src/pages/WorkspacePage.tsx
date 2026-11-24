import { useEffect, useState } from 'react'
import {
  AlertTriangle,
  ArrowLeft,
  ChevronDown,
  Columns2,
  Download,
  FileText,
  List,
  Loader2,
  MoreHorizontal,
  PanelLeft,
  PanelRight,
  Pause,
  Play,
  Redo2,
  RefreshCw,
  Settings,
  SlidersHorizontal,
  Undo2,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import { api } from '../api/client'
import { useWorkspaceStore, type BlockTranslationUpdate } from '../store/useWorkspaceStore'
import type { ExportMode, ViewMode } from '../types/document'
import { DualPaneViewer } from '../components/viewer/DualPaneViewer'
import { BlockInspector } from '../components/editor/BlockInspector'
import { SegmentListView } from '../components/editor/SegmentListView'

interface Props {
  docId: string
  onBack: () => void
  onOpenSettings: () => void
}

type RangeMode = 'all' | 'current' | 'custom'

export function WorkspacePage({ docId, onBack, onOpenSettings }: Props) {
  const {
    doc,
    loading,
    error,
    viewMode,
    scale,
    selectedBlockId,
    activePageIndex,
    saveState,
    past,
    future,
    loadDocument,
    clearDocument,
    setViewMode,
    setScale,
    undo,
    redo,
    applyBlocksBatch,
    applyJobStatus,
    bumpCleanBgVersion,
  } = useWorkspaceStore()

  // 翻译配置状态
  const [stylePreset, setStylePreset] = useState('general')
  const [targetLang, setTargetLang] = useState('zh-CN')
  const [rangeMode, setRangeMode] = useState<RangeMode>('all')
  const [customRange, setCustomRange] = useState('1-5')
  const [retranslateExisting, setRetranslateExisting] = useState(false)

  // 现代工作台面板与浮层控制
  const [leftNavOpen, setLeftNavOpen] = useState(true)
  const [inspectorOpen, setInspectorOpen] = useState(false)
  const [activePopover, setActivePopover] = useState<'translate' | 'export' | 'more' | null>(null)
  const [exporting, setExporting] = useState(false)
  const [toast, setToast] = useState<string | null>(null)
  const [scrollToPage, setScrollToPage] = useState<number | null>(null)

  // 选中任意块时自动展开右侧检查器，未选中时释放双栏阅读视野
  useEffect(() => {
    if (selectedBlockId) {
      setInspectorOpen(true)
    }
  }, [selectedBlockId])

  useEffect(() => {
    void loadDocument(docId)
    return () => clearDocument()
  }, [docId, loadDocument, clearDocument])

  useEffect(() => {
    if (!doc || (doc.status !== 'uploaded' && doc.status !== 'parsing')) return
    const t = setInterval(() => {
      void loadDocument(docId).then(() => {
        const cur = useWorkspaceStore.getState().doc
        if (cur && cur.status !== 'parsing' && cur.status !== 'uploaded') {
          bumpCleanBgVersion()
        }
      })
    }, 1500)
    return () => clearInterval(t)
  }, [doc?.status, docId, loadDocument, bumpCleanBgVersion])

  // 订阅 SSE 流式翻译事件（150ms 节流合批，避免高并发 SSE 轰炸主线程）
  useEffect(() => {
    const es = new EventSource(`/api/documents/${docId}/translate/events`)
    const pendingUpdates: BlockTranslationUpdate[] = []
    let latestCompleted: number | undefined
    let latestTotal: number | undefined
    let flushTimer: ReturnType<typeof setTimeout> | null = null

    const flush = () => {
      if (flushTimer !== null) {
        clearTimeout(flushTimer)
        flushTimer = null
      }
      if (pendingUpdates.length === 0) return
      const batch = pendingUpdates.splice(0, pendingUpdates.length)
      applyBlocksBatch(batch, latestCompleted, latestTotal)
    }

    const scheduleFlush = () => {
      if (flushTimer === null) {
        flushTimer = setTimeout(flush, 150)
      }
    }

    es.addEventListener('block_translated', (ev) => {
      try {
        const d = JSON.parse((ev as MessageEvent).data)
        pendingUpdates.push({
          blockId: d.block_id,
          pageIndex: d.page_index,
          translatedText: d.translated_text,
          status: d.status,
        })
        latestCompleted = d.completed_blocks
        latestTotal = d.total_blocks
        scheduleFlush()
      } catch {
        // 忽略畸形事件
      }
    })
    es.addEventListener('block_failed', (ev) => {
      try {
        const d = JSON.parse((ev as MessageEvent).data)
        pendingUpdates.push({
          blockId: d.block_id,
          pageIndex: d.page_index,
          status: 'failed',
        })
        scheduleFlush()
      } catch {
        // 忽略
      }
    })
    es.addEventListener('job_completed', (ev) => {
      try {
        flush()
        const d = JSON.parse((ev as MessageEvent).data)
        applyJobStatus(d.status)
        if (d.status === 'paused') {
          setToast('翻译已暂停，进度已保存')
        } else if (d.page_range) {
          setToast(`第 ${d.page_range} 页翻译完成（全篇累计 ${d.completed_blocks}/${d.total_blocks} 块）`)
        } else {
          setToast(`翻译完成（成功 ${d.completed_blocks} 块，失败 ${d.failed_blocks} 块）`)
        }
      } catch {
        // 忽略
      }
    })
    return () => {
      if (flushTimer !== null) clearTimeout(flushTimer)
      es.close()
    }
  }, [docId, applyBlocksBatch, applyJobStatus])

  // 全局 Undo / Redo 与 Escape 关闭弹层
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setActivePopover(null)
      }
      const target = e.target as HTMLElement | null
      if (target && (target.tagName === 'TEXTAREA' || target.tagName === 'INPUT')) return
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'z') {
        e.preventDefault()
        if (e.shiftKey) redo()
        else undo()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [undo, redo])

  useEffect(() => {
    if (!toast) return
    const t = setTimeout(() => setToast(null), 4000)
    return () => clearTimeout(t)
  }, [toast])

  const resolvePageRangeStr = (): string => {
    if (rangeMode === 'current') return String(activePageIndex + 1)
    if (rangeMode === 'custom') return customRange.trim()
    return ''
  }

  const handleStartTranslate = async (overrideRange?: string, forceRetranslate?: boolean) => {
    if (!doc) return
    const pageRange = overrideRange !== undefined ? overrideRange : resolvePageRangeStr()
    if (overrideRange === undefined && rangeMode === 'custom' && !pageRange) {
      setToast('请输入要翻译的页码范围（如 1-5 或 1,3,5-8）')
      setActivePopover('translate')
      return
    }
    setActivePopover(null)
    try {
      await api.startTranslate(doc.id, {
        target_lang: targetLang,
        style_preset: stylePreset,
        page_range: pageRange,
        retranslate: forceRetranslate ?? retranslateExisting,
      })
      applyJobStatus('translating')
      setToast(
        pageRange ? `已启动第 ${pageRange} 页并发流式翻译...` : '已启动整篇文档并发流式翻译...',
      )
    } catch (err) {
      setToast(err instanceof Error ? err.message : '启动翻译失败')
    }
  }

  const handlePauseTranslate = async () => {
    if (!doc) return
    await api.pauseTranslate(doc.id)
    setToast('正在优雅暂停当前批次...')
  }

  const handleReparse = async () => {
    if (!doc) return
    setActivePopover(null)
    if (!window.confirm('将使用最新排版引擎重新解析版面并重建纯净底图（会清空当前译文），确定继续吗？')) {
      return
    }
    try {
      await api.reparseDocument(doc.id)
      applyJobStatus('parsing')
      setToast('已触发重新解析，请稍候...')
    } catch (err) {
      setToast(err instanceof Error ? err.message : '重新解析失败')
    }
  }

  const handleExport = async (mode: ExportMode) => {
    if (!doc) return
    setActivePopover(null)
    setExporting(true)
    try {
      await api.exportDocument(doc.id, mode, doc.filename)
      setToast('高保真 PDF 已生成并开始下载')
    } catch (err) {
      setToast(err instanceof Error ? err.message : '导出失败')
    } finally {
      setExporting(false)
    }
  }

  if (loading && !doc) {
    return (
      <div className="flex h-full items-center justify-center bg-slate-50 text-slate-500">
        <Loader2 className="mr-2 h-5 w-5 animate-spin text-indigo-600" />
        正在加载文档工作台...
      </div>
    )
  }

  if (error || !doc) {
    return (
      <div className="flex h-full flex-col items-center justify-center gap-3 bg-slate-50 text-slate-600">
        <AlertTriangle className="h-8 w-8 text-amber-500" />
        <p>{error || '文档不存在'}</p>
        <button
          onClick={onBack}
          className="rounded-xl bg-indigo-600 px-4 py-2 text-sm font-medium text-white"
        >
          返回文档库
        </button>
      </div>
    )
  }

  const pct =
    doc.total_blocks > 0 ? Math.round((doc.completed_blocks / doc.total_blocks) * 100) : 0
  const isTranslating = doc.status === 'translating'
  const canTranslate = doc.status !== 'parsing' && !doc.is_encrypted && !doc.is_scanned
  const rangeSummaryLabel =
    rangeMode === 'current'
      ? `第 ${activePageIndex + 1} 页`
      : rangeMode === 'custom'
        ? `${customRange.trim() || '指定'} 页`
        : '全篇'

  return (
    <div className="flex h-full flex-col overflow-hidden bg-slate-100">
      {/* 点击外部关闭弹层的透明遮罩 */}
      {activePopover && (
        <div
          className="fixed inset-0 z-20"
          onClick={() => setActivePopover(null)}
        />
      )}

      {/* 现代化单行三段式顶部工具栏 */}
      <header className="z-30 flex h-13 shrink-0 items-center justify-between gap-3 border-b border-slate-200/90 bg-white/95 px-3.5 backdrop-blur-md">
        {/* 左侧：返回、文档标题与紧凑进度胶囊 */}
        <div className="flex min-w-0 items-center gap-2">
          <button
            onClick={onBack}
            className="rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-900"
            title="返回文档库"
          >
            <ArrowLeft className="h-4 w-4" />
          </button>

          {viewMode !== 'list' && (
            <button
              onClick={() => setLeftNavOpen((v) => !v)}
              className={`rounded-lg p-1.5 transition ${
                leftNavOpen
                  ? 'bg-slate-100 text-slate-800'
                  : 'text-slate-500 hover:bg-slate-100 hover:text-slate-800'
              }`}
              title={leftNavOpen ? '收起页面导航' : '展开页面导航'}
            >
              <PanelLeft className="h-4 w-4" />
            </button>
          )}

          <div className="min-w-0 max-w-[180px] sm:max-w-[240px]">
            <h1 className="truncate text-sm font-semibold text-slate-900" title={doc.filename}>
              {doc.filename}
            </h1>
          </div>

          <div
            className="hidden items-center gap-2 rounded-full border border-slate-200/80 bg-slate-50 px-2.5 py-1 text-[11px] text-slate-600 md:inline-flex"
            title={`已翻译 ${doc.completed_blocks} / ${doc.total_blocks} 块`}
          >
            <span className="h-1.5 w-12 overflow-hidden rounded-full bg-slate-200">
              <span
                className="block h-full rounded-full bg-indigo-600 transition-all"
                style={{ width: `${pct}%` }}
              />
            </span>
            <span className="font-mono font-medium text-slate-700">{pct}%</span>
            {saveState === 'saving' && <span className="text-amber-600">· 保存中</span>}
            {saveState === 'saved' && <span className="text-emerald-600">· 已保存</span>}
            {saveState === 'error' && <span className="text-red-600">· 保存失败</span>}
          </div>
        </div>

        {/* 中间：Segmented Control 视图切换 + 缩放 */}
        <div className="flex items-center gap-2">
          <div className="inline-flex rounded-xl border border-slate-200/80 bg-slate-100/80 p-0.5">
            {(
              [
                { k: 'dual', label: '双栏对照', icon: Columns2 },
                { k: 'translated', label: '纯译文', icon: FileText },
                { k: 'list', label: '段落列表', icon: List },
              ] as Array<{ k: ViewMode; label: string; icon: typeof Columns2 }>
            ).map(({ k, label, icon: Icon }) => (
              <button
                key={k}
                onClick={() => setViewMode(k)}
                className={`inline-flex items-center gap-1.5 rounded-lg px-2.5 py-1 text-xs font-medium transition ${
                  viewMode === k
                    ? 'bg-white text-indigo-700 shadow-2xs'
                    : 'text-slate-600 hover:text-slate-900'
                }`}
              >
                <Icon className="h-3.5 w-3.5" />
                <span className="hidden sm:inline">{label}</span>
              </button>
            ))}
          </div>

          {viewMode !== 'list' && (
            <div className="hidden items-center rounded-xl border border-slate-200/80 bg-white px-1 py-0.5 lg:inline-flex">
              <button
                onClick={() => setScale(scale - 0.15)}
                className="rounded-md p-1 text-slate-500 hover:bg-slate-100"
                title="缩小"
              >
                <ZoomOut className="h-3.5 w-3.5" />
              </button>
              <button
                onClick={() => setScale(1.0)}
                className="w-11 text-center font-mono text-[11px] font-medium text-slate-700 hover:text-indigo-600"
                title="点击复位 100%"
              >
                {Math.round(scale * 100)}%
              </button>
              <button
                onClick={() => setScale(scale + 0.15)}
                className="rounded-md p-1 text-slate-500 hover:bg-slate-100"
                title="放大"
              >
                <ZoomIn className="h-3.5 w-3.5" />
              </button>
            </div>
          )}
        </div>

        {/* 右侧：翻译分体按钮 + 导出 + 辅助面板 */}
        <div className="flex items-center gap-1.5">
          {/* 翻译控制区（带范围/风格浮层 Popover） */}
          <div className="relative">
            {isTranslating ? (
              <button
                onClick={handlePauseTranslate}
                className="inline-flex items-center gap-1.5 rounded-xl bg-amber-500 px-3 py-1.5 text-xs font-medium text-white shadow-xs transition hover:bg-amber-600"
              >
                <span className="h-2 w-2 animate-pulse rounded-full bg-white" />
                <Pause className="h-3.5 w-3.5" />
                暂停翻译
              </button>
            ) : (
              <div className="inline-flex items-center overflow-hidden rounded-xl bg-indigo-600 shadow-xs shadow-indigo-500/20">
                <button
                  disabled={!canTranslate}
                  onClick={() => void handleStartTranslate()}
                  className="inline-flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-white transition hover:bg-indigo-700 disabled:opacity-45"
                  title="按当前范围与配置开始翻译"
                >
                  <Play className="h-3.5 w-3.5" />
                  <span>翻译 · {rangeSummaryLabel}</span>
                </button>
                <button
                  disabled={!canTranslate}
                  onClick={() =>
                    setActivePopover((p) => (p === 'translate' ? null : 'translate'))
                  }
                  className="border-l border-indigo-500/70 px-2 py-1.5 text-white/90 transition hover:bg-indigo-700 disabled:opacity-45"
                  title="选择翻译页码范围与风格设置"
                >
                  <SlidersHorizontal className="h-3.5 w-3.5" />
                </button>
              </div>
            )}

            {/* 翻译配置 Popover 浮层 */}
            {activePopover === 'translate' && (
              <div className="absolute right-0 z-40 mt-2 w-80 rounded-2xl border border-slate-200 bg-white p-4 shadow-xl">
                <div className="mb-3 flex items-center justify-between">
                  <h3 className="text-xs font-semibold text-slate-900">翻译范围与偏好设置</h3>
                  <span className="text-[11px] text-slate-400">共 {doc.page_count} 页</span>
                </div>

                {/* 范围三选一 */}
                <div className="mb-3 grid grid-cols-3 gap-1 rounded-xl bg-slate-100 p-1">
                  {(
                    [
                      { k: 'current', label: `当前 P${activePageIndex + 1}` },
                      { k: 'custom', label: '指定页码' },
                      { k: 'all', label: '整篇文档' },
                    ] as Array<{ k: RangeMode; label: string }>
                  ).map((item) => (
                    <button
                      key={item.k}
                      type="button"
                      onClick={() => setRangeMode(item.k)}
                      className={`rounded-lg py-1.5 text-xs font-medium transition ${
                        rangeMode === item.k
                          ? 'bg-white text-indigo-700 shadow-2xs'
                          : 'text-slate-600 hover:text-slate-900'
                      }`}
                    >
                      {item.label}
                    </button>
                  ))}
                </div>

                {/* 自定义页码输入框 */}
                {rangeMode === 'custom' && (
                  <div className="mb-3 space-y-1.5">
                    <input
                      value={customRange}
                      onChange={(e) => setCustomRange(e.target.value)}
                      placeholder="例如: 1-5 或 1,3,5-8"
                      className="w-full rounded-xl border border-slate-200 px-3 py-1.5 font-mono text-xs focus:border-indigo-500 focus:ring-2 focus:ring-indigo-500/15 focus:outline-none"
                    />
                    <div className="flex flex-wrap gap-1">
                      {[
                        { label: '前 3 页', val: `1-${Math.min(3, doc.page_count)}` },
                        { label: '前 5 页', val: `1-${Math.min(5, doc.page_count)}` },
                        { label: '前 10 页', val: `1-${Math.min(10, doc.page_count)}` },
                        {
                          label: '当前后3页',
                          val: `${activePageIndex + 1}-${Math.min(activePageIndex + 3, doc.page_count)}`,
                        },
                      ].map((pre) => (
                        <button
                          key={pre.label}
                          type="button"
                          onClick={() => setCustomRange(pre.val)}
                          className="rounded-md bg-slate-100 px-2 py-0.5 text-[11px] text-slate-600 hover:bg-indigo-50 hover:text-indigo-700"
                        >
                          {pre.label}
                        </button>
                      ))}
                    </div>
                  </div>
                )}

                <div className="mb-3 grid grid-cols-2 gap-2">
                  <div>
                    <label className="mb-1 block text-[11px] text-slate-500">目标语言</label>
                    <select
                      value={targetLang}
                      onChange={(e) => setTargetLang(e.target.value)}
                      className="w-full rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-xs text-slate-700"
                    >
                      <option value="zh-CN">简体中文</option>
                      <option value="en">English</option>
                      <option value="ja">日本語</option>
                    </select>
                  </div>
                  <div>
                    <label className="mb-1 block text-[11px] text-slate-500">翻译风格</label>
                    <select
                      value={stylePreset}
                      onChange={(e) => setStylePreset(e.target.value)}
                      className="w-full rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-xs text-slate-700"
                    >
                      <option value="general">通用专业</option>
                      <option value="academic">学术论文</option>
                      <option value="technical">技术手册</option>
                      <option value="business">商务合同</option>
                    </select>
                  </div>
                </div>

                <label className="mb-3 flex cursor-pointer items-center justify-between rounded-xl border border-slate-200/80 bg-slate-50/70 px-3 py-2 text-xs text-slate-700">
                  <span>覆盖重译（默认仅翻未完成段）</span>
                  <input
                    type="checkbox"
                    checked={retranslateExisting}
                    onChange={(e) => setRetranslateExisting(e.target.checked)}
                    className="h-4 w-4 accent-indigo-600"
                  />
                </label>

                <button
                  type="button"
                  onClick={() => void handleStartTranslate()}
                  className="inline-flex w-full items-center justify-center gap-1.5 rounded-xl bg-indigo-600 py-2 text-xs font-medium text-white shadow-xs transition hover:bg-indigo-700"
                >
                  <Play className="h-3.5 w-3.5" />
                  立即开始翻译（{rangeSummaryLabel}）
                </button>
              </div>
            )}
          </div>

          {/* 导出下拉 */}
          <div className="relative">
            <button
              disabled={exporting || doc.status === 'parsing'}
              onClick={() => setActivePopover((p) => (p === 'export' ? null : 'export'))}
              className="inline-flex items-center gap-1 rounded-xl border border-slate-200 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-700 transition hover:bg-slate-50 disabled:opacity-50"
            >
              {exporting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Download className="h-3.5 w-3.5" />
              )}
              <span className="hidden sm:inline">导出</span>
              <ChevronDown className="h-3 w-3 text-slate-400" />
            </button>
            {activePopover === 'export' && (
              <div className="absolute right-0 z-40 mt-2 w-56 rounded-2xl border border-slate-200 bg-white p-1.5 shadow-xl">
                <button
                  onClick={() => void handleExport('translated')}
                  className="flex w-full flex-col rounded-xl px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">纯译文保真 PDF</span>
                  <span className="text-[11px] text-slate-500">保留全部图片与矢量线</span>
                </button>
                <button
                  onClick={() => void handleExport('bilingual_interleaved')}
                  className="flex w-full flex-col rounded-xl px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">双语对照（奇原偶译）</span>
                  <span className="text-[11px] text-slate-500">奇数页原文、偶数页译文</span>
                </button>
                <button
                  onClick={() => void handleExport('bilingual_side_by_side')}
                  className="flex w-full flex-col rounded-xl px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">双语对照（左右并排）</span>
                  <span className="text-[11px] text-slate-500">宽屏单页左右拼接</span>
                </button>
              </div>
            )}
          </div>

          <div className="mx-0.5 hidden h-4 w-px bg-slate-200 sm:block" />

          <button
            disabled={past.length === 0}
            onClick={undo}
            title="撤销 (Cmd/Ctrl+Z)"
            className="hidden rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 disabled:opacity-30 sm:inline-flex"
          >
            <Undo2 className="h-4 w-4" />
          </button>
          <button
            disabled={future.length === 0}
            onClick={redo}
            title="重做 (Cmd/Ctrl+Shift+Z)"
            className="hidden rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-800 disabled:opacity-30 sm:inline-flex"
          >
            <Redo2 className="h-4 w-4" />
          </button>

          <button
            onClick={() => setInspectorOpen((v) => !v)}
            className={`rounded-lg p-1.5 transition ${
              inspectorOpen
                ? 'bg-indigo-50 text-indigo-700'
                : 'text-slate-500 hover:bg-slate-100 hover:text-slate-800'
            }`}
            title={inspectorOpen ? '收起检查器面板' : '展开检查器面板'}
          >
            <PanelRight className="h-4 w-4" />
          </button>

          {/* 更多操作菜单（收纳重新解析与模型设置） */}
          <div className="relative">
            <button
              onClick={() => setActivePopover((p) => (p === 'more' ? null : 'more'))}
              className="rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-800"
              title="更多操作"
            >
              <MoreHorizontal className="h-4 w-4" />
            </button>
            {activePopover === 'more' && (
              <div className="absolute right-0 z-40 mt-2 w-48 rounded-2xl border border-slate-200 bg-white p-1.5 shadow-xl">
                <button
                  disabled={doc.status === 'parsing'}
                  onClick={() => void handleReparse()}
                  className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100 disabled:opacity-45"
                >
                  <RefreshCw className={`h-3.5 w-3.5 ${doc.status === 'parsing' ? 'animate-spin' : ''}`} />
                  重新解析版面
                </button>
                <button
                  onClick={() => {
                    setActivePopover(null)
                    onOpenSettings()
                  }}
                  className="flex w-full items-center gap-2 rounded-xl px-3 py-2 text-left text-xs font-medium text-slate-700 hover:bg-slate-100"
                >
                  <Settings className="h-3.5 w-3.5" />
                  AI 模型设置
                </button>
              </div>
            )}
          </div>
        </div>
      </header>

      {/* 异常/解析状态横幅 */}
      {(doc.status === 'parsing' || doc.error_message) && (
        <div
          className={`flex items-center gap-2 px-5 py-2 text-xs ${
            doc.status === 'parsing'
              ? 'bg-indigo-50 text-indigo-700'
              : 'bg-amber-50 text-amber-800'
          }`}
        >
          {doc.status === 'parsing' ? (
            <>
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
              正在通过纯 Go 自研 PDF 引擎解析版面并生成无损底图...
            </>
          ) : (
            <>
              <AlertTriangle className="h-3.5 w-3.5" />
              {doc.error_message}
            </>
          )}
        </div>
      )}

      {/* 主工作区 */}
      <div className="flex flex-1 overflow-hidden">
        {/* 左侧页码导航栏（支持折叠 + 悬停一键翻单页） */}
        {viewMode !== 'list' && leftNavOpen && (
          <aside className="flex w-24 shrink-0 flex-col items-center overflow-y-auto border-r border-slate-200/90 bg-white/95 px-2 py-3">
            <span className="mb-2.5 text-[10px] font-semibold tracking-wider text-slate-400 uppercase">
              共 {doc.pages.length} 页
            </span>
            <div className="w-full space-y-1.5">
              {doc.pages.map((p) => {
                const total = p.blocks.filter((b) => b.translatable).length
                const done = p.blocks.filter(
                  (b) =>
                    b.translatable &&
                    (b.status === 'translated' || b.status === 'edited' || b.status === 'reviewed'),
                ).length
                const pagePct = total > 0 ? Math.round((done / total) * 100) : 0
                const active = activePageIndex === p.page_index

                return (
                  <div
                    key={p.page_index}
                    onClick={() => setScrollToPage(p.page_index)}
                    className={`group relative flex cursor-pointer flex-col rounded-xl border px-2 py-2 transition ${
                      active
                        ? 'border-indigo-500 bg-indigo-50/70 text-indigo-700 shadow-2xs'
                        : 'border-slate-200/80 bg-slate-50/60 text-slate-600 hover:border-slate-300 hover:bg-white'
                    }`}
                  >
                    <div className="flex items-center justify-between text-xs font-semibold">
                      <span>P{p.page_index + 1}</span>
                      {!isTranslating && canTranslate && total > 0 && (
                        <button
                          onClick={(e) => {
                            e.stopPropagation()
                            void handleStartTranslate(String(p.page_index + 1), done === total)
                          }}
                          title={done === total ? `重译第 ${p.page_index + 1} 页` : `仅翻译第 ${p.page_index + 1} 页`}
                          className="flex h-5 w-5 items-center justify-center rounded-md bg-indigo-600 text-white opacity-0 shadow-2xs transition hover:bg-indigo-700 group-hover:opacity-100"
                        >
                          <Play className="h-2.5 w-2.5 fill-current" />
                        </button>
                      )}
                    </div>
                    {total > 0 && (
                      <>
                        <span className="mt-0.5 text-[10px] text-slate-400">
                          {done}/{total}
                        </span>
                        <div className="mt-1 h-1 w-full overflow-hidden rounded-full bg-slate-200/80">
                          <div
                            className={`h-full rounded-full transition-all ${
                              pagePct === 100 ? 'bg-emerald-500' : 'bg-indigo-500'
                            }`}
                            style={{ width: `${pagePct}%` }}
                          />
                        </div>
                      </>
                    )}
                  </div>
                )
              })}
            </div>
          </aside>
        )}

        {/* 中央画布或 CAT 列表 */}
        {viewMode === 'list' ? <SegmentListView /> : <DualPaneViewer scrollToPage={scrollToPage} />}

        {/* 右侧属性检查器（可收起释放视野） */}
        <BlockInspector open={inspectorOpen} onClose={() => setInspectorOpen(false)} />
      </div>

      {/* 底部轻提示 */}
      {toast && (
        <div className="fixed bottom-5 left-1/2 z-50 -translate-x-1/2 rounded-full bg-slate-900/90 px-4 py-2 text-xs font-medium text-white shadow-lg backdrop-blur">
          {toast}
        </div>
      )}
    </div>
  )
}
