import { useEffect, useState } from 'react'
import {
  AlertTriangle,
  BookOpen,
  FileText,
  Loader2,
  Search,
  Settings,
  Trash2,
} from 'lucide-react'
import { api } from '../api/client'
import { renderFirstPageThumbnail } from '../lib/pdfjs'
import type { DocumentSummary } from '../types/document'
import { UploadDropzone } from '../components/library/UploadDropzone'

interface Props {
  onOpenDocument: (docId: string) => void
  onOpenSettings: () => void
}

const STATUS_BADGE: Record<string, { label: string; cls: string }> = {
  uploaded: { label: '已上传', cls: 'bg-slate-100 text-slate-600' },
  parsing: { label: '解析中', cls: 'bg-amber-100 text-amber-700' },
  parsed: { label: '已解析', cls: 'bg-sky-100 text-sky-700' },
  translating: { label: '翻译中', cls: 'bg-indigo-100 text-indigo-700' },
  paused: { label: '已暂停', cls: 'bg-amber-100 text-amber-800' },
  completed: { label: '已完成', cls: 'bg-emerald-100 text-emerald-700' },
  failed: { label: '异常', cls: 'bg-red-100 text-red-700' },
}

export function HomePage({ onOpenDocument, onOpenSettings }: Props) {
  const [docs, setDocs] = useState<DocumentSummary[]>([])
  const [search, setSearch] = useState('')
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = async () => {
    try {
      const list = await api.listDocuments()
      setDocs(list)
    } catch (err) {
      setError(err instanceof Error ? err.message : '加载文档列表失败')
    }
  }

  useEffect(() => {
    void refresh()
    const timer = setInterval(() => {
      void refresh()
    }, 3000)
    return () => clearInterval(timer)
  }, [])

  const handleUpload = async (file: File) => {
    setError(null)
    setUploading(true)
    try {
      const created = await api.uploadDocument(file)
      // 浏览器端使用 PDF.js 生成首页缩略图并回传后端缓存
      void renderFirstPageThumbnail(api.originalPdfUrl(created.id)).then((blob) => {
        if (blob) {
          void api.uploadThumbnail(created.id, blob).then(() => refresh())
        }
      })
      await refresh()
      onOpenDocument(created.id)
    } catch (err) {
      setError(err instanceof Error ? err.message : '上传失败')
    } finally {
      setUploading(false)
    }
  }

  const handleDelete = async (doc: DocumentSummary) => {
    if (!window.confirm(`确定要永久删除「${doc.filename}」及其全部翻译记录吗？`)) return
    try {
      await api.deleteDocument(doc.id)
      await refresh()
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除失败')
    }
  }

  const filtered = docs.filter((d) =>
    d.filename.toLowerCase().includes(search.trim().toLowerCase()),
  )

  return (
    <div className="min-h-full bg-slate-50">
      {/* 顶栏 */}
      <header className="sticky top-0 z-20 border-b border-slate-200 bg-white/90 backdrop-blur">
        <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-3.5">
          <div className="flex items-center gap-2.5">
            <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-indigo-600 text-white shadow-sm">
              <BookOpen className="h-5 w-5" />
            </div>
            <div>
              <h1 className="text-base font-bold tracking-tight text-slate-900">
                TranslationAssistant
              </h1>
              <p className="text-[11px] text-slate-500">AI 高保真文档翻译与校对工作台</p>
            </div>
          </div>

          <button
            onClick={onOpenSettings}
            className="inline-flex items-center gap-2 rounded-lg border border-slate-200 bg-white px-3.5 py-2 text-sm font-medium text-slate-700 shadow-2xs transition hover:border-indigo-300 hover:bg-indigo-50/50 hover:text-indigo-700"
          >
            <Settings className="h-4 w-4" />
            模型设置
          </button>
        </div>
      </header>

      <main className="mx-auto max-w-6xl space-y-8 px-6 py-8">
        {error && (
          <div className="flex items-center gap-2 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <span>{error}</span>
          </div>
        )}

        <UploadDropzone uploading={uploading} onUpload={handleUpload} />

        {/* 文档列表 */}
        <section>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <h2 className="text-base font-semibold text-slate-800">
              我的文档 <span className="text-sm font-normal text-slate-400">({docs.length})</span>
            </h2>
            <div className="relative w-64">
              <Search className="pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2 text-slate-400" />
              <input
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="搜索文档名..."
                className="w-full rounded-lg border border-slate-200 bg-white py-1.5 pr-3 pl-9 text-sm focus:border-indigo-500 focus:outline-none"
              />
            </div>
          </div>

          {filtered.length === 0 ? (
            <div className="rounded-2xl border border-slate-200 bg-white py-16 text-center text-sm text-slate-400">
              {docs.length === 0 ? '暂无文档，上传一份 PDF 开始体验高保真 AI 翻译' : '没有匹配的文档'}
            </div>
          ) : (
            <div className="grid grid-cols-1 gap-5 sm:grid-cols-2 lg:grid-cols-3">
              {filtered.map((doc) => {
                const badge = STATUS_BADGE[doc.status] ?? STATUS_BADGE.uploaded
                const pct =
                  doc.total_blocks > 0
                    ? Math.round((doc.completed_blocks / doc.total_blocks) * 100)
                    : 0

                return (
                  <div
                    key={doc.id}
                    onClick={() => onOpenDocument(doc.id)}
                    className="group flex cursor-pointer flex-col overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xs transition hover:-translate-y-0.5 hover:border-indigo-300 hover:shadow-md"
                  >
                    <div className="relative flex h-44 items-center justify-center overflow-hidden border-b border-slate-100 bg-slate-100">
                      {doc.has_thumbnail ? (
                        <img
                          src={api.thumbnailUrl(doc.id)}
                          alt={doc.filename}
                          className="h-full w-full object-cover object-top"
                        />
                      ) : (
                        <FileText className="h-12 w-12 text-slate-300" />
                      )}
                      <span
                        className={`absolute top-3 right-3 inline-flex items-center gap-1 rounded-full px-2.5 py-0.5 text-xs font-medium shadow-xs ${badge.cls}`}
                      >
                        {(doc.status === 'parsing' || doc.status === 'translating') && (
                          <Loader2 className="h-3 w-3 animate-spin" />
                        )}
                        {badge.label}
                      </span>
                    </div>

                    <div className="flex flex-1 flex-col justify-between p-4">
                      <div>
                        <h3
                          className="truncate text-sm font-semibold text-slate-800 group-hover:text-indigo-700"
                          title={doc.filename}
                        >
                          {doc.filename}
                        </h3>
                        <p className="mt-1 text-xs text-slate-500">
                          {doc.page_count} 页 · {doc.completed_blocks}/{doc.total_blocks} 段落 ({pct}%)
                        </p>
                        {doc.error_message && (
                          <p className="mt-1.5 line-clamp-2 text-xs text-amber-700">
                            {doc.error_message}
                          </p>
                        )}
                      </div>

                      <div className="mt-3 space-y-2.5">
                        <div className="h-1.5 w-full overflow-hidden rounded-full bg-slate-100">
                          <div
                            className="h-full rounded-full bg-indigo-600 transition-all"
                            style={{ width: `${pct}%` }}
                          />
                        </div>
                        <div className="flex items-center justify-between text-xs text-slate-400">
                          <span>{new Date(doc.updated_at).toLocaleString('zh-CN')}</span>
                          <button
                            onClick={(e) => {
                              e.stopPropagation()
                              void handleDelete(doc)
                            }}
                            className="rounded p-1 text-slate-400 hover:bg-red-50 hover:text-red-600"
                            title="删除文档"
                          >
                            <Trash2 className="h-4 w-4" />
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </section>
      </main>
    </div>
  )
}
