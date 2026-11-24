import { memo, useCallback, useEffect, useRef, useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { useWorkspaceStore } from '../../store/useWorkspaceStore'
import type { TranslationBlock } from '../../types/document'

interface Props {
  blocks: TranslationBlock[]
  scale: number
  mode: 'original' | 'translated'
}

export const TranslationOverlay = memo(function TranslationOverlay({ blocks, scale, mode }: Props) {
  const selectedBlockId = useWorkspaceStore((s) => s.selectedBlockId)
  const hoveredBlockId = useWorkspaceStore((s) => s.hoveredBlockId)
  const selectBlock = useWorkspaceStore((s) => s.selectBlock)
  const hoverBlock = useWorkspaceStore((s) => s.hoverBlock)
  const updateBlockLocal = useWorkspaceStore((s) => s.updateBlockLocal)

  const [editingId, setEditingId] = useState<string | null>(null)

  // 使用稳定引用回调，确保 memo(EditableTranslatedBox) 在非选中块上 100% 跳过重渲染
  const handleSelect = useCallback((id: string) => selectBlock(id), [selectBlock])
  const handleHover = useCallback((id: string | null) => hoverBlock(id), [hoverBlock])
  const handleStartEdit = useCallback(
    (id: string) => {
      selectBlock(id)
      setEditingId(id)
    },
    [selectBlock],
  )
  const handleStopEdit = useCallback(() => setEditingId(null), [])
  const handleChangeText = useCallback(
    (id: string, txt: string) => updateBlockLocal(id, { translated_text: txt }),
    [updateBlockLocal],
  )
  const handleChangeBBox = useCallback(
    (id: string, bbox: [number, number, number, number]) => updateBlockLocal(id, { bbox }, true),
    [updateBlockLocal],
  )

  return (
    <>
      {blocks.map((blk) => {
        const isSelected = selectedBlockId === blk.id
        const isHovered = hoveredBlockId === blk.id
        const isEditing = mode === 'translated' && editingId === blk.id

        const left = blk.bbox[0] * scale
        const top = blk.bbox[1] * scale
        const width = Math.max(12, (blk.bbox[2] - blk.bbox[0]) * scale)
        const height = Math.max(12, (blk.bbox[3] - blk.bbox[1]) * scale)

        // 原文侧仅显示轻量交互高亮框
        if (mode === 'original') {
          return (
            <div
              key={blk.id}
              style={{ left, top, width, height }}
              onMouseEnter={() => hoverBlock(blk.id)}
              onMouseLeave={() => hoverBlock(null)}
              onClick={(e) => {
                e.stopPropagation()
                selectBlock(blk.id)
              }}
              className={`pointer-events-auto absolute cursor-pointer rounded-[2px] transition-colors ${
                isSelected
                  ? 'bg-indigo-500/18 ring-2 ring-indigo-500'
                  : isHovered
                    ? 'bg-sky-400/15 ring-1 ring-sky-400'
                    : 'hover:bg-sky-400/10'
              }`}
            />
          )
        }

        // 译文侧：若该块不参与翻译，仅在悬停/选中时显示虚线轮廓（底图保留了原文）
        if (!blk.translatable) {
          return (
            <div
              key={blk.id}
              style={{ left, top, width, height }}
              onMouseEnter={() => hoverBlock(blk.id)}
              onMouseLeave={() => hoverBlock(null)}
              onClick={(e) => {
                e.stopPropagation()
                selectBlock(blk.id)
              }}
              className={`pointer-events-auto absolute cursor-pointer rounded-[2px] transition ${
                isSelected
                  ? 'ring-2 ring-amber-500'
                  : isHovered
                    ? 'ring-1 ring-dashed ring-slate-400'
                    : ''
              }`}
              title="已跳过翻译（原样保留）"
            />
          )
        }

        return (
          <EditableTranslatedBox
            key={blk.id}
            block={blk}
            scale={scale}
            left={left}
            top={top}
            width={width}
            height={height}
            isSelected={isSelected}
            isHovered={isHovered}
            isEditing={isEditing}
            onSelect={handleSelect}
            onHover={handleHover}
            onStartEdit={handleStartEdit}
            onStopEdit={handleStopEdit}
            onChangeText={handleChangeText}
            onChangeBBox={handleChangeBBox}
          />
        )
      })}
    </>
  )
})

interface BoxProps {
  block: TranslationBlock
  scale: number
  left: number
  top: number
  width: number
  height: number
  isSelected: boolean
  isHovered: boolean
  isEditing: boolean
  onSelect: (id: string) => void
  onHover: (id: string | null) => void
  onStartEdit: (id: string) => void
  onStopEdit: () => void
  onChangeText: (id: string, text: string) => void
  onChangeBBox: (id: string, bbox: [number, number, number, number]) => void
}

const FONT_STACK: Record<string, string> = {
  serif: 'Georgia, "Times New Roman", "Songti SC", "STSong", "Noto Serif SC", serif',
  mono: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace',
  sans: 'system-ui, -apple-system, "PingFang SC", "Noto Sans SC", "Helvetica Neue", sans-serif',
}

const EditableTranslatedBox = memo(function EditableTranslatedBox({
  block,
  scale,
  left,
  top,
  width,
  height,
  isSelected,
  isHovered,
  isEditing,
  onSelect,
  onHover,
  onStartEdit,
  onStopEdit,
  onChangeText,
  onChangeBBox,
}: BoxProps) {
  const textRef = useRef<HTMLDivElement>(null)
  const [overflow, setOverflow] = useState(false)
  const baseFontSize = (block.style.translated_font_size || block.style.font_size || 10.5) * scale
  const baseLineHeight = block.style.line_height || 1.25
  const [fitFontSize, setFitFontSize] = useState(baseFontSize)
  const [fitLineHeight, setFitLineHeight] = useState(baseLineHeight)

  const displayText = block.translated_text || block.original_text
  const isUntranslated = !block.translated_text
  const isSingleLineBox = height <= baseFontSize * 1.65 && !displayText.includes('\n')
  const tocMatch =
    block.block_type === 'toc_item' && !displayText.includes('\n')
      ? displayText.match(/^(.+?)\s*(?:\.\s*){3,}(\d+)\s*$/)
      : null
  const [keepNoWrap, setKeepNoWrap] = useState(isSingleLineBox)

  // 自适应字号与行高检测：
  // 1. 单行原位块按真实横向 scrollWidth 精确缩至贴合（不截断首尾字符）；
  // 2. 多行段落温和压缩字号和行高，确保高度严格贴合原框。
  useEffect(() => {
    setFitFontSize(baseFontSize)
    setFitLineHeight(baseLineHeight)
    setKeepNoWrap(isSingleLineBox)

    let cancelled = false
    const id = requestAnimationFrame(() => {
      if (cancelled) return
      const el = textRef.current
      if (!el) return
      const minSize = Math.max(5.5 * scale, baseFontSize * 0.55)

      if (isSingleLineBox) {
        if (el.scrollWidth > width + 1) {
          const scaled = baseFontSize * (width / Math.max(el.scrollWidth, 1)) * 0.985
          if (scaled >= minSize) {
            setFitFontSize(scaled)
            setOverflow(false)
          } else {
            setKeepNoWrap(false)
            setFitFontSize(minSize)
            requestAnimationFrame(() => {
              if (!cancelled && textRef.current) {
                setOverflow(textRef.current.scrollHeight > height + 8)
              }
            })
          }
        } else {
          setOverflow(false)
        }
        return
      }

      if (el.scrollHeight > height + 2) {
        const ratio = Math.sqrt(height / Math.max(el.scrollHeight, 1)) * 0.98
        const nextSize = Math.max(minSize, baseFontSize * ratio)
        const nextLh = Math.max(1.04, Math.min(baseLineHeight, baseLineHeight * 0.96))
        setFitFontSize(nextSize)
        setFitLineHeight(nextLh)
        requestAnimationFrame(() => {
          if (cancelled || !textRef.current) return
          const el2 = textRef.current
          if (el2.scrollHeight > height + 2 && nextSize > minSize) {
            const ratio2 = Math.sqrt(height / Math.max(el2.scrollHeight, 1)) * 0.98
            const nextSize2 = Math.max(minSize, nextSize * ratio2)
            setFitFontSize(nextSize2)
            requestAnimationFrame(() => {
              if (!cancelled && textRef.current) {
                setOverflow(textRef.current.scrollHeight > height + 10)
              }
            })
          } else {
            setOverflow(el2.scrollHeight > height + 10)
          }
        })
      } else {
        setOverflow(false)
      }
    })
    return () => {
      cancelled = true
      cancelAnimationFrame(id)
    }
  }, [displayText, baseFontSize, baseLineHeight, width, height, scale, isSingleLineBox])

  // 右下角拖拽微调 BBox 尺寸
  const handleResizeMouseDown = (e: React.MouseEvent) => {
    e.stopPropagation()
    e.preventDefault()
    const startX = e.clientX
    const startY = e.clientY
    const [x0, y0, x1, y1] = block.bbox

    const onMove = (ev: MouseEvent) => {
      const dx = (ev.clientX - startX) / scale
      const dy = (ev.clientY - startY) / scale
      const nextX1 = Math.max(x0 + 20, x1 + dx)
      const nextY1 = Math.max(y0 + 12, y1 + dy)
      onChangeBBox(block.id, [
        Number(x0.toFixed(2)),
        Number(y0.toFixed(2)),
        Number(nextX1.toFixed(2)),
        Number(nextY1.toFixed(2)),
      ])
    }
    const onUp = () => {
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  const fontFamily = FONT_STACK[block.style.font_family || 'sans'] || FONT_STACK.sans

  return (
    <div
      style={{
        left,
        top,
        width,
        height,
        fontFamily,
        fontSize: `${fitFontSize}px`,
        lineHeight: isSingleLineBox && keepNoWrap ? 1.05 : fitLineHeight,
        fontWeight: block.style.font_weight === 'bold' ? 700 : 400,
        fontStyle: block.style.font_style === 'italic' ? 'italic' : 'normal',
        color: block.style.color || '#111827',
        opacity: isUntranslated && !isEditing ? 0.58 : 1,
        textAlign: block.style.align || 'left',
      }}
      onMouseEnter={() => onHover(block.id)}
      onMouseLeave={() => onHover(null)}
      onClick={(e) => {
        e.stopPropagation()
        onSelect(block.id)
      }}
      onDoubleClick={(e) => {
        e.stopPropagation()
        onStartEdit(block.id)
      }}
      className={`pointer-events-auto absolute rounded-[2px] transition-shadow ${
        isSelected
          ? 'z-20 bg-white/80 ring-2 ring-indigo-500'
          : isHovered
            ? 'z-10 bg-indigo-50/40 ring-1 ring-indigo-400'
            : block.status === 'failed'
              ? 'bg-red-50/60 ring-1 ring-red-400'
              : 'bg-transparent'
      }`}
    >
      {isEditing ? (
        <textarea
          autoFocus
          value={block.translated_text}
          onChange={(e) => onChangeText(block.id, e.target.value)}
          onBlur={onStopEdit}
          onKeyDown={(e) => {
            if (e.key === 'Escape') onStopEdit()
          }}
          style={{
            fontFamily,
            fontSize: `${fitFontSize}px`,
            lineHeight: fitLineHeight,
            textAlign: block.style.align || 'left',
          }}
          className="h-full w-full resize-none bg-white/95 p-0.5 text-slate-900 focus:outline-none"
        />
      ) : tocMatch ? (
        <div
          ref={textRef}
          className="flex h-full w-full items-center overflow-hidden select-none"
        >
          <span className="shrink-0 whitespace-nowrap">{tocMatch[1].trim()}</span>
          <span className="mx-1.5 mb-[0.22em] min-w-3 flex-1 border-b border-dotted border-current opacity-65" />
          <span className="shrink-0 whitespace-nowrap">{tocMatch[2].trim()}</span>
        </div>
      ) : (
        <div
          ref={textRef}
          className={`h-full w-full select-none ${
            keepNoWrap
              ? `flex items-center overflow-visible whitespace-nowrap ${
                  block.style.align === 'center'
                    ? 'justify-center'
                    : block.style.align === 'right'
                      ? 'justify-end'
                      : 'justify-start'
                }`
              : 'overflow-hidden break-words whitespace-pre-wrap'
          }`}
        >
          {displayText}
        </div>
      )}

      {overflow && !isUntranslated && !isEditing && (isHovered || isSelected) && (
        <span
          title="译文略微超出原文本框，可拖动右下角拉大或点击右侧 AI 精简"
          className="absolute -top-2 -right-2 flex h-4 w-4 items-center justify-center rounded-full bg-amber-500 text-white shadow"
        >
          <AlertCircle className="h-3 w-3" />
        </span>
      )}

      {isSelected && !isEditing && (
        <div
          onMouseDown={handleResizeMouseDown}
          title="拖拽调整文本框尺寸"
          className="absolute -right-1.5 -bottom-1.5 h-3 w-3 cursor-nwse-resize rounded-full border-2 border-white bg-indigo-600 shadow"
        />
      )}
    </div>
  )
})
