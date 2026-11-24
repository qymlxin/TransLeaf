import * as pdfjsLib from 'pdfjs-dist'
import workerSrc from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

// 使用 Vite 本地打包的 PDF.js Worker，完全离线可用且符合 Apache-2.0 协议
pdfjsLib.GlobalWorkerOptions.workerSrc = workerSrc

export { pdfjsLib }

// 在浏览器端将 PDF 第一页渲染为 PNG Blob 用于首页文档卡片缩略图缓存
export async function renderFirstPageThumbnail(pdfUrl: string): Promise<Blob | null> {
  try {
    const task = pdfjsLib.getDocument(pdfUrl)
    const pdf = await task.promise
    const page = await pdf.getPage(1)
    const baseViewport = page.getViewport({ scale: 1 })
    const scale = 320 / Math.max(baseViewport.width, 1)
    const viewport = page.getViewport({ scale })

    const canvas = document.createElement('canvas')
    canvas.width = Math.round(viewport.width)
    canvas.height = Math.round(viewport.height)
    const ctx = canvas.getContext('2d')
    if (!ctx) return null

    await page.render({ canvasContext: ctx, viewport }).promise
    await pdf.destroy()

    return await new Promise<Blob | null>((resolve) =>
      canvas.toBlob((blob) => resolve(blob), 'image/png'),
    )
  } catch {
    return null
  }
}
