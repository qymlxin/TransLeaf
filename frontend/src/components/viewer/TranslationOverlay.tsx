import { useEffect, useRef, useState } from 'react'
import { AlertCircle } from 'lucide-react'
import { useWorkspaceStore } from '../../store/useWorkspaceStore'
import type { TranslationBlock } from '../../types/document'

interface Props {
  blocks: TranslationBlock[]
  scale: number
  mode: 'original' | 'translated'
}

export function TranslationOverlay({ blocks, scale, mode }: Props) {
  const {
    selectedBlockId,
    hoveredBlockId,
    selectBlock,
    hoverBlock,
    updateBlockLocal,
  } = useWorkspaceStore()

  const [editingId, setEditingId] = useState<string | null>(null)

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
            onSelect={() => selectBlock(blk.id)}
            onHover={(h) => hoverBlock(h ? blk.id : null)}
            onStartEdit={() => {
              selectBlock(blk.id)
              setEditingId(blk.id)
            }}
            onStopEdit={() => setEditingId(null)}
            onChangeText={(txt) => updateBlockLocal(blk.id, { translated_text: txt })}
            onChangeBBox={(bbox) => updateBlockLocal(blk.id, { bbox }, true)}
          />
        )
      })}
    </>
  )
}

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
  onSelect: () => void
  onHover: (hovered: boolean) => void
  onStartEdit: () => void
  onStopEdit: () => void
  onChangeText: (text: string) => void
  onChangeBBox: (bbox: [number, number, number, number]) => void
}

function EditableTranslatedBox({
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
  const [fitFontSize, setFitFontSize] = useState(baseFontSize)

  const displayText = block.translated_text || block.original_text
  const isUntranslated = !block.translated_text
  const isSingleLineBox = height <= baseFontSize * 1.65 && !displayText.includes('\n')
  const [keepNoWrap, setKeepNoWrap] = useState(isSingleLineBox)

  // 自适应字号检测：单行原位块（页眉/表格单元格/图注）优先水平微缩保单行，多行块按高度适配
  useEffect(() => {
    setFitFontSize(baseFontSize)
    setKeepNoWrap(isSingleLineBox)
    const id = requestAnimationFrame(() => {
      const el = textRef.current
      if (!el) return
      const minSize = Math.max(6 * scale, baseFontSize * 0.6)
      if (isSingleLineBox) {
        if (el.scrollWidth > width + 3) {
          const scaled = baseFontSize * (width / Math.max(el.scrollWidth, 1))
          if (scaled >= minSize) {
            setFitFontSize(scaled)
            setOverflow(false)
          } else {
            setKeepNoWrap(false)
            setFitFontSize(minSize)
            requestAnimationFrame(() => {
              if (textRef.current) {
                setOverflow(textRef.current.scrollHeight > height + 4)
              }
            })
          }
        } else {
          setOverflow(false)
        }
        return
      }

      if (el.scrollHeight > height + 3) {
        const ratio = Math.sqrt(height / Math.max(el.scrollHeight, 1))
        const next = Math.max(minSize, baseFontSize * ratio)
        setFitFontSize(next)
        requestAnimationFrame(() => {
          if (textRef.current) {
            setOverflow(textRef.current.scrollHeight > height + 4)
          }
        })
      } else {
        setOverflow(false)
      }
    })
    return () => cancelAnimationFrame(id)
  }, [displayText, baseFontSize, width, height, scale, block.style.line_height, isSingleLineBox])

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
      onChangeBBox([
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

  return (
    <div
      style={{
        left,
        top,
        width,
        height,
        fontSize: `${fitFontSize}px`,
        lineHeight: block.style.line_height || 1.25,
        fontWeight: block.style.font_weight === 'bold' ? 700 : 400,
        fontStyle: block.style.font_style === 'italic' ? 'italic' : 'normal',
        color: isUntranslated ? '#94a3b8' : block.style.color || '#111827',
        textAlign: block.style.align || 'left',
      }}
      onMouseEnter={() => onHover(true)}
      onMouseLeave={() => onHover(false)}
      onClick={(e) => {
        e.stopPropagation()
        onSelect()
      }}
      onDoubleClick={(e) => {
        e.stopPropagation()
        onStartEdit()
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
          onChange={(e) => onChangeText(e.target.value)}
          onBlur={onStopEdit}
          onKeyDown={(e) => {
            if (e.key === 'Escape') onStopEdit()
          }}
          style={{
            fontSize: `${fitFontSize}px`,
            lineHeight: block.style.line_height || 1.25,
            textAlign: block.style.align || 'left',
          }}
          className="h-full w-full resize-none bg-white/95 p-0.5 text-slate-900 focus:outline-none"
        />
      ) : (
        <div
          ref={textRef}
          className={`h-full w-full overflow-hidden select-none ${
            keepNoWrap ? 'whitespace-nowrap' : 'break-words whitespace-pre-wrap'
          }`}
        >
          {displayText}
        </div>
      )}

      {overflow && !isEditing && (
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
}
