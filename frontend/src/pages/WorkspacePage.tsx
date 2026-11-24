import { useEffect, useState } from 'react'
import {
  AlertTriangle,
  ArrowLeft,
  Columns2,
  Download,
  FileText,
  List,
  Loader2,
  Pause,
  Play,
  Redo2,
  RefreshCw,
  Settings,
  Undo2,
  ZoomIn,
  ZoomOut,
} from 'lucide-react'
import { api } from '../api/client'
import { useWorkspaceStore } from '../store/useWorkspaceStore'
import type { ExportMode, ViewMode } from '../types/document'
import { DualPaneViewer } from '../components/viewer/DualPaneViewer'
import { BlockInspector } from '../components/editor/BlockInspector'
import { SegmentListView } from '../components/editor/SegmentListView'

interface Props {
  docId: string
  onBack: () => void
  onOpenSettings: () => void
}

export function WorkspacePage({ docId, onBack, onOpenSettings }: Props) {
  const {
    doc,
    loading,
    error,
    viewMode,
    scale,
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
    applyBlockTranslated,
    applyBlockFailed,
    applyJobStatus,
    bumpCleanBgVersion,
  } = useWorkspaceStore()

  const [stylePreset, setStylePreset] = useState('general')
  const [targetLang, setTargetLang] = useState('zh-CN')
  const [exportOpen, setExportOpen] = useState(false)
  const [exporting, setExporting] = useState(false)
  const [toast, setToast] = useState<string | null>(null)
  const [scrollToPage, setScrollToPage] = useState<number | null>(null)

  // 加载文档并在解析中时自动轮询直至解析完成
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

  // 订阅 SSE 流式翻译事件
  useEffect(() => {
    const es = new EventSource(`/api/documents/${docId}/translate/events`)
    es.addEventListener('block_translated', (ev) => {
      try {
        const d = JSON.parse((ev as MessageEvent).data)
        applyBlockTranslated(
          d.block_id,
          d.translated_text,
          d.status,
          d.completed_blocks,
          d.total_blocks,
        )
      } catch {
        // 忽略畸形事件
      }
    })
    es.addEventListener('block_failed', (ev) => {
      try {
        const d = JSON.parse((ev as MessageEvent).data)
        applyBlockFailed(d.block_id)
      } catch {
        // 忽略
      }
    })
    es.addEventListener('job_completed', (ev) => {
      try {
        const d = JSON.parse((ev as MessageEvent).data)
        applyJobStatus(d.status)
        setToast(
          d.status === 'paused'
            ? '翻译已暂停，进度已保存'
            : `翻译完成（成功 ${d.completed_blocks} 块，失败 ${d.failed_blocks} 块）`,
        )
      } catch {
        // 忽略
      }
    })
    return () => es.close()
  }, [docId, applyBlockTranslated, applyBlockFailed, applyJobStatus])

  // 全局 Undo / Redo 快捷键
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
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

  const handleStartTranslate = async () => {
    if (!doc) return
    try {
      await api.startTranslate(doc.id, {
        target_lang: targetLang,
        style_preset: stylePreset,
      })
      applyJobStatus('translating')
      setToast('已启动并发流式翻译...')
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
    setExportOpen(false)
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
          className="rounded-lg bg-indigo-600 px-4 py-2 text-sm font-medium text-white"
        >
          返回文档库
        </button>
      </div>
    )
  }

  const pct =
    doc.total_blocks > 0 ? Math.round((doc.completed_blocks / doc.total_blocks) * 100) : 0
  const isTranslating = doc.status === 'translating'

  return (
    <div className="flex h-full flex-col overflow-hidden bg-slate-100">
      {/* 顶部工具栏 */}
      <header className="z-30 flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-slate-200 bg-white px-4 py-2.5 shadow-2xs">
        <div className="flex items-center gap-2.5">
          <button
            onClick={onBack}
            className="rounded-lg p-1.5 text-slate-500 hover:bg-slate-100 hover:text-slate-800"
            title="返回文档库"
          >
            <ArrowLeft className="h-4 w-4" />
          </button>
          <div className="max-w-[200px] sm:max-w-xs">
            <h1 className="truncate text-sm font-semibold text-slate-900" title={doc.filename}>
              {doc.filename}
            </h1>
            <div className="flex items-center gap-2 text-[11px] text-slate-500">
              <span>
                {doc.completed_blocks}/{doc.total_blocks} 块 ({pct}%)
              </span>
              {saveState === 'saving' && <span className="text-amber-600">保存中...</span>}
              {saveState === 'saved' && <span className="text-emerald-600">已保存</span>}
              {saveState === 'error' && <span className="text-red-600">保存失败</span>}
            </div>
          </div>
        </div>

        {/* 视图切换 */}
        <div className="inline-flex rounded-lg border border-slate-200 bg-slate-50 p-0.5">
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
              className={`inline-flex items-center gap-1 rounded-md px-2.5 py-1 text-xs font-medium transition ${
                viewMode === k
                  ? 'bg-white text-indigo-700 shadow-2xs'
                  : 'text-slate-600 hover:text-slate-900'
              }`}
            >
              <Icon className="h-3.5 w-3.5" />
              {label}
            </button>
          ))}
        </div>

        {/* 缩放与撤销/重做 */}
        <div className="flex items-center gap-1">
          {viewMode !== 'list' && (
            <div className="mr-1 inline-flex items-center rounded-lg border border-slate-200 bg-white px-1 py-0.5">
              <button
                onClick={() => setScale(scale - 0.15)}
                className="rounded p-1 text-slate-500 hover:bg-slate-100"
                title="缩小"
              >
                <ZoomOut className="h-3.5 w-3.5" />
              </button>
              <span className="w-11 text-center font-mono text-xs text-slate-700">
                {Math.round(scale * 100)}%
              </span>
              <button
                onClick={() => setScale(scale + 0.15)}
                className="rounded p-1 text-slate-500 hover:bg-slate-100"
                title="放大"
              >
                <ZoomIn className="h-3.5 w-3.5" />
              </button>
            </div>
          )}

          <button
            disabled={past.length === 0}
            onClick={undo}
            title="撤销 (Cmd/Ctrl+Z)"
            className="rounded-lg p-1.5 text-slate-600 hover:bg-slate-100 disabled:opacity-35"
          >
            <Undo2 className="h-4 w-4" />
          </button>
          <button
            disabled={future.length === 0}
            onClick={redo}
            title="重做 (Cmd/Ctrl+Shift+Z)"
            className="rounded-lg p-1.5 text-slate-600 hover:bg-slate-100 disabled:opacity-35"
          >
            <Redo2 className="h-4 w-4" />
          </button>
        </div>

        {/* 翻译控制与导出 */}
        <div className="flex items-center gap-2">
          <select
            value={targetLang}
            onChange={(e) => setTargetLang(e.target.value)}
            className="rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-xs text-slate-700"
          >
            <option value="zh-CN">译为 简体中文</option>
            <option value="en">译为 English</option>
            <option value="ja">译为 日本語</option>
          </select>

          <select
            value={stylePreset}
            onChange={(e) => setStylePreset(e.target.value)}
            className="rounded-lg border border-slate-200 bg-white px-2 py-1.5 text-xs text-slate-700"
          >
            <option value="general">通用风格</option>
            <option value="academic">学术论文</option>
            <option value="technical">技术文档</option>
            <option value="business">商务合同</option>
          </select>

          {isTranslating ? (
            <button
              onClick={handlePauseTranslate}
              className="inline-flex items-center gap-1.5 rounded-lg bg-amber-500 px-3 py-1.5 text-xs font-medium text-white hover:bg-amber-600"
            >
              <Pause className="h-3.5 w-3.5" /> 暂停翻译
            </button>
          ) : (
            <button
              disabled={doc.status === 'parsing' || doc.is_encrypted || doc.is_scanned}
              onClick={handleStartTranslate}
              className="inline-flex items-center gap-1.5 rounded-lg bg-indigo-600 px-3 py-1.5 text-xs font-medium text-white hover:bg-indigo-700 disabled:opacity-45"
            >
              <Play className="h-3.5 w-3.5" />
              {doc.completed_blocks > 0 && doc.completed_blocks < doc.total_blocks
                ? '继续翻译'
                : '开始翻译'}
            </button>
          )}

          {/* 导出下拉 */}
          <div className="relative">
            <button
              disabled={exporting || doc.status === 'parsing'}
              onClick={() => setExportOpen((v) => !v)}
              className="inline-flex items-center gap-1.5 rounded-lg border border-slate-300 bg-white px-3 py-1.5 text-xs font-medium text-slate-700 hover:bg-slate-50 disabled:opacity-50"
            >
              {exporting ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Download className="h-3.5 w-3.5" />
              )}
              导出 PDF
            </button>
            {exportOpen && (
              <div className="absolute right-0 mt-1.5 w-52 rounded-xl border border-slate-200 bg-white p-1.5 shadow-xl">
                <button
                  onClick={() => void handleExport('translated')}
                  className="flex w-full flex-col rounded-lg px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">纯译文保真 PDF</span>
                  <span className="text-[11px] text-slate-500">保留全部图片与矢量线</span>
                </button>
                <button
                  onClick={() => void handleExport('bilingual_interleaved')}
                  className="flex w-full flex-col rounded-lg px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">双语对照（奇原偶译）</span>
                  <span className="text-[11px] text-slate-500">奇数页原文、偶数页译文</span>
                </button>
                <button
                  onClick={() => void handleExport('bilingual_side_by_side')}
                  className="flex w-full flex-col rounded-lg px-3 py-2 text-left hover:bg-indigo-50"
                >
                  <span className="text-xs font-semibold text-slate-800">双语对照（左右并排）</span>
                  <span className="text-[11px] text-slate-500">宽屏单页左右拼接</span>
                </button>
              </div>
            )}
          </div>

          <button
            disabled={doc.status === 'parsing'}
            onClick={() => void handleReparse()}
            className="inline-flex items-center gap-1 rounded-lg border border-slate-200 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-600 hover:bg-slate-50 hover:text-indigo-700 disabled:opacity-45"
            title="使用最新引擎重新解析版面"
          >
            <RefreshCw className={`h-3.5 w-3.5 ${doc.status === 'parsing' ? 'animate-spin' : ''}`} />
            重新解析
          </button>

          <button
            onClick={onOpenSettings}
            className="rounded-lg p-1.5 text-slate-500 hover:bg-slate-100 hover:text-slate-800"
            title="模型设置"
          >
            <Settings className="h-4 w-4" />
          </button>
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
        {/* 左侧页码导航栏 */}
        {viewMode !== 'list' && (
          <aside className="flex w-20 shrink-0 flex-col items-center overflow-y-auto border-r border-slate-200 bg-white py-3">
            <span className="mb-2 text-[10px] font-medium text-slate-400">
              共 {doc.pages.length} 页
            </span>
            <div className="space-y-2">
              {doc.pages.map((p) => {
                const total = p.blocks.filter((b) => b.translatable).length
                const done = p.blocks.filter(
                  (b) =>
                    b.translatable &&
                    (b.status === 'translated' || b.status === 'edited' || b.status === 'reviewed'),
                ).length
                const active = activePageIndex === p.page_index
                return (
                  <button
                    key={p.page_index}
                    onClick={() => setScrollToPage(p.page_index)}
                    className={`flex h-12 w-12 flex-col items-center justify-center rounded-lg border text-xs transition ${
                      active
                        ? 'border-indigo-500 bg-indigo-50 font-semibold text-indigo-700'
                        : 'border-slate-200 bg-slate-50 text-slate-600 hover:border-slate-300'
                    }`}
                  >
                    <span>P{p.page_index + 1}</span>
                    {total > 0 && (
                      <span className="text-[9px] text-slate-400">
                        {done}/{total}
                      </span>
                    )}
                  </button>
                )
              })}
            </div>
          </aside>
        )}

        {/* 中央画布或 CAT 列表 */}
        {viewMode === 'list' ? <SegmentListView /> : <DualPaneViewer scrollToPage={scrollToPage} />}

        {/* 右侧属性检查器 */}
        <BlockInspector />
      </div>

      {/* 底部轻提示 */}
      {toast && (
        <div className="fixed bottom-5 left-1/2 z-50 -translate-x-1/2 rounded-full bg-slate-900/90 px-4 py-2 text-xs font-medium text-white shadow-lg">
          {toast}
        </div>
      )}
    </div>
  )
}
