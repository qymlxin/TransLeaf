import { memo, useEffect, useRef, useState } from 'react'
import type { PDFDocumentProxy } from 'pdfjs-dist'

interface Props {
  pdfDoc: PDFDocumentProxy | null
  pageIndex: number // 0-based
  width: number     // PDF points
  height: number    // PDF points
  scale: number
  children?: React.ReactNode
}

// 基于 IntersectionObserver 的按需虚拟挂载 + Retina 高清 PDF.js 单页渲染组件
// 仅对视口附近（上下缓冲 800px）页面挂载译文叠加层 DOM，彻底避免长文档（100+ 页）卡顿
export const PdfPageCanvas = memo(function PdfPageCanvas({
  pdfDoc,
  pageIndex,
  width,
  height,
  scale,
  children,
}: Props) {
  const containerRef = useRef<HTMLDivElement>(null)
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [visible, setVisible] = useState(pageIndex < 3)

  useEffect(() => {
    const el = containerRef.current
    if (!el) return
    const observer = new IntersectionObserver(
      (entries) => {
        setVisible(Boolean(entries[0]?.isIntersecting))
      },
      { rootMargin: '800px 0px' },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (!visible || !pdfDoc || !canvasRef.current) return
    let cancelled = false
    let renderTask: { cancel: () => void; promise: Promise<void> } | null = null

    const render = async () => {
      try {
        const page = await pdfDoc.getPage(pageIndex + 1)
        if (cancelled) return
        const dpr = Math.min(window.devicePixelRatio || 1, 2)
        const viewport = page.getViewport({ scale })
        const canvas = canvasRef.current
        if (!canvas) return
        const ctx = canvas.getContext('2d')
        if (!ctx) return

        canvas.width = Math.floor(viewport.width * dpr)
        canvas.height = Math.floor(viewport.height * dpr)
        canvas.style.width = `${viewport.width}px`
        canvas.style.height = `${viewport.height}px`

        ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
        renderTask = page.render({ canvasContext: ctx, viewport })
        await renderTask.promise
      } catch {
        // 忽略取消渲染异常
      }
    }
    void render()

    return () => {
      cancelled = true
      renderTask?.cancel()
    }
  }, [pdfDoc, pageIndex, scale, visible])

  const cssW = Math.round(width * scale)
  const cssH = Math.round(height * scale)

  return (
    <div
      ref={containerRef}
      style={{ width: cssW, height: cssH }}
      className="relative mx-auto mb-5 overflow-hidden rounded-sm bg-white shadow-md ring-1 ring-slate-200/80"
    >
      <canvas ref={canvasRef} className="block" />
      {visible && <div className="pointer-events-none absolute inset-0">{children}</div>}
    </div>
  )
})
