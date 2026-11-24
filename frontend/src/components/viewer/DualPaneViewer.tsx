import { useEffect, useRef, useState } from 'react'
import type { PDFDocumentProxy } from 'pdfjs-dist'
import { api } from '../../api/client'
import { pdfjsLib } from '../../lib/pdfjs'
import { useWorkspaceStore } from '../../store/useWorkspaceStore'
import { PdfPageCanvas } from './PdfPageCanvas'
import { TranslationOverlay } from './TranslationOverlay'

interface Props {
  scrollToPage?: number | null
}

// 双底图同步预览视图：左侧渲染 original.pdf，右侧渲染无损擦除底图 clean_bg.pdf + 透明可编辑译文层
export function DualPaneViewer({ scrollToPage }: Props) {
  const { doc, viewMode, scale, cleanBgVersion, selectBlock, setActivePage } = useWorkspaceStore()

  const [origPdf, setOrigPdf] = useState<PDFDocumentProxy | null>(null)
  const [cleanPdf, setCleanPdf] = useState<PDFDocumentProxy | null>(null)

  const leftScrollRef = useRef<HTMLDivElement>(null)
  const rightScrollRef = useRef<HTMLDivElement>(null)
  const scrollLockRef = useRef<'left' | 'right' | null>(null)

  // 加载原版 PDF
  useEffect(() => {
    if (!doc) return
    let alive = true
    let loaded: PDFDocumentProxy | null = null
    const task = pdfjsLib.getDocument(api.originalPdfUrl(doc.id))
    task.promise
      .then((pdf) => {
        if (!alive) {
          void pdf.destroy()
          return
        }
        loaded = pdf
        setOrigPdf(pdf)
      })
      .catch(() => {})

    return () => {
      alive = false
      void loaded?.destroy()
    }
  }, [doc?.id])

  // 加载纯净底图 clean_bg.pdf（当切换块可译状态时通过 cleanBgVersion 触发刷新）
  useEffect(() => {
    if (!doc) return
    let alive = true
    let loaded: PDFDocumentProxy | null = null
    const task = pdfjsLib.getDocument(api.cleanBgPdfUrl(doc.id, cleanBgVersion))
    task.promise
      .then((pdf) => {
        if (!alive) {
          void pdf.destroy()
          return
        }
        loaded = pdf
        setCleanPdf(pdf)
      })
      .catch(() => {})

    return () => {
      alive = false
      void loaded?.destroy()
    }
  }, [doc?.id, cleanBgVersion])

  // 响应左侧缩略图页码跳转
  useEffect(() => {
    if (scrollToPage == null) return
    const leftEl = leftScrollRef.current?.querySelector(`[data-page="${scrollToPage}"]`)
    const rightEl = rightScrollRef.current?.querySelector(`[data-page="${scrollToPage}"]`)
    leftEl?.scrollIntoView({ behavior: 'smooth', block: 'start' })
    rightEl?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, [scrollToPage])

  const syncScroll = (source: 'left' | 'right') => {
    if (scrollLockRef.current && scrollLockRef.current !== source) return
    scrollLockRef.current = source

    const srcEl = source === 'left' ? leftScrollRef.current : rightScrollRef.current
    const dstEl = source === 'left' ? rightScrollRef.current : leftScrollRef.current
    if (srcEl && dstEl) {
      dstEl.scrollTop = srcEl.scrollTop
      dstEl.scrollLeft = srcEl.scrollLeft
    }

    // 估算当前可见页码
    if (srcEl && doc && doc.pages.length > 0) {
      const avgH = srcEl.scrollHeight / doc.pages.length
      if (avgH > 0) {
        const idx = Math.min(doc.pages.length - 1, Math.max(0, Math.floor(srcEl.scrollTop / avgH)))
        setActivePage(idx)
      }
    }

    requestAnimationFrame(() => {
      scrollLockRef.current = null
    })
  }

  if (!doc) return null

  return (
    <div
      className="flex flex-1 overflow-hidden bg-slate-200/70"
      onClick={() => selectBlock(null)}
    >
      {/* 左侧原版 PDF（仅在双栏对照模式显示） */}
      {viewMode === 'dual' && (
        <div
          ref={leftScrollRef}
          onScroll={() => syncScroll('left')}
          className="h-full flex-1 overflow-auto border-r border-slate-300/80 px-5 py-6"
        >
          <div className="mb-3 text-center text-xs font-medium text-slate-500">
            原版 PDF（点击段落同步定位）
          </div>
          {doc.pages.map((p) => (
            <div key={`orig-${p.page_index}`} data-page={p.page_index}>
              <PdfPageCanvas
                pdfDoc={origPdf}
                pageIndex={p.page_index}
                width={p.width}
                height={p.height}
                scale={scale}
              >
                <TranslationOverlay blocks={p.blocks} scale={scale} mode="original" />
              </PdfPageCanvas>
            </div>
          ))}
        </div>
      )}

      {/* 右侧保真译文画布（纯净底图 clean_bg.pdf + 透明可编辑译文层） */}
      <div
        ref={rightScrollRef}
        onScroll={() => syncScroll('right')}
        className="h-full flex-1 overflow-auto px-5 py-6"
      >
        <div className="mb-3 text-center text-xs font-medium text-slate-500">
          保真译文画布（双击任意段落原地编辑 · 选中可拖拽调整大小）
        </div>
        {doc.pages.map((p) => (
          <div key={`trans-${p.page_index}`} data-page={p.page_index}>
            <PdfPageCanvas
              pdfDoc={cleanPdf ?? origPdf}
              pageIndex={p.page_index}
              width={p.width}
              height={p.height}
              scale={scale}
            >
              <TranslationOverlay blocks={p.blocks} scale={scale} mode="translated" />
            </PdfPageCanvas>
          </div>
        ))}
      </div>
    </div>
  )
}
