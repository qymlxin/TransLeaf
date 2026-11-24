import { create } from 'zustand'
import { api } from '../api/client'
import type {
  BlockStatus,
  BlockStyle,
  DocumentIR,
  TranslationBlock,
  ViewMode,
} from '../types/document'

interface HistoryEntry {
  blockId: string
  before: TranslationBlock
  after: TranslationBlock
}

export interface BlockTranslationUpdate {
  blockId: string
  pageIndex?: number
  translatedText?: string
  status: BlockStatus
}

interface WorkspaceState {
  doc: DocumentIR | null
  loading: boolean
  error: string | null
  viewMode: ViewMode
  scale: number
  selectedBlockId: string | null
  hoveredBlockId: string | null
  activePageIndex: number
  cleanBgVersion: number
  saveState: 'idle' | 'saving' | 'saved' | 'error'
  past: HistoryEntry[]
  future: HistoryEntry[]

  loadDocument: (docId: string) => Promise<void>
  clearDocument: () => void
  setViewMode: (mode: ViewMode) => void
  setScale: (scale: number) => void
  selectBlock: (blockId: string | null) => void
  hoverBlock: (blockId: string | null) => void
  setActivePage: (pageIndex: number) => void
  bumpCleanBgVersion: () => void

  // 本地编辑（带历史栈与 800ms 防抖落盘）
  updateBlockLocal: (
    blockId: string,
    patch: Partial<{
      translated_text: string
      bbox: [number, number, number, number]
      style: BlockStyle
      translatable: boolean
      status: BlockStatus
    }>,
    immediate?: boolean,
  ) => void

  undo: () => void
  redo: () => void

  // SSE 实时批量更新回调
  applyBlocksBatch: (
    updates: BlockTranslationUpdate[],
    completedBlocks?: number,
    totalBlocks?: number,
  ) => void
  applyBlockTranslated: (
    blockId: string,
    translatedText: string,
    status: BlockStatus,
    completedBlocks: number,
    totalBlocks: number,
  ) => void
  applyBlockFailed: (blockId: string) => void
  applyJobStatus: (status: DocumentIR['status']) => void
}

const debounceTimers = new Map<string, ReturnType<typeof setTimeout>>()

function findBlock(doc: DocumentIR, blockId: string): TranslationBlock | null {
  for (const p of doc.pages) {
    const found = p.blocks.find((b) => b.id === blockId)
    if (found) return found
  }
  return null
}

function isBlockDone(status: BlockStatus): boolean {
  return status === 'translated' || status === 'edited' || status === 'reviewed'
}

// 结构共享更新单块：仅拷贝目标页引用，未变更页面保持原引用，避免 React 全量重绘
function replaceBlock(doc: DocumentIR, nextBlock: TranslationBlock): DocumentIR {
  let deltaCompleted = 0
  let deltaTotal = 0
  const pages = doc.pages.map((p) => {
    if (p.page_index !== nextBlock.page_index) return p
    const blocks = p.blocks.map((b) => {
      if (b.id !== nextBlock.id) return b
      if (b.translatable !== nextBlock.translatable) {
        deltaTotal += nextBlock.translatable ? 1 : -1
      }
      const wasDone = b.translatable && isBlockDone(b.status)
      const nowDone = nextBlock.translatable && isBlockDone(nextBlock.status)
      if (wasDone !== nowDone) {
        deltaCompleted += nowDone ? 1 : -1
      }
      return nextBlock
    })
    return { ...p, blocks }
  })
  return {
    ...doc,
    pages,
    total_blocks: Math.max(0, doc.total_blocks + deltaTotal),
    completed_blocks: Math.max(0, doc.completed_blocks + deltaCompleted),
  }
}

export const useWorkspaceStore = create<WorkspaceState>((set, get) => ({
  doc: null,
  loading: false,
  error: null,
  viewMode: 'dual',
  scale: 1.1,
  selectedBlockId: null,
  hoveredBlockId: null,
  activePageIndex: 0,
  cleanBgVersion: 0,
  saveState: 'idle',
  past: [],
  future: [],

  loadDocument: async (docId: string) => {
    set({ loading: true, error: null })
    try {
      const ir = await api.getDocumentIR(docId)
      set({
        doc: ir,
        loading: false,
        selectedBlockId: null,
        hoveredBlockId: null,
        activePageIndex: 0,
        past: [],
        future: [],
      })
    } catch (err) {
      set({
        loading: false,
        error: err instanceof Error ? err.message : '加载文档失败',
      })
    }
  },

  clearDocument: () => {
    for (const t of debounceTimers.values()) clearTimeout(t)
    debounceTimers.clear()
    set({ doc: null, selectedBlockId: null, past: [], future: [] })
  },

  setViewMode: (viewMode) => set({ viewMode }),
  setScale: (scale) => set({ scale: Math.max(0.5, Math.min(2.2, Number(scale.toFixed(2)))) }),
  selectBlock: (selectedBlockId) => set({ selectedBlockId }),
  hoverBlock: (hoveredBlockId) => set({ hoveredBlockId }),
  setActivePage: (activePageIndex) => set({ activePageIndex }),
  bumpCleanBgVersion: () => set((s) => ({ cleanBgVersion: s.cleanBgVersion + 1 })),

  updateBlockLocal: (blockId, patch, immediate = false) => {
    const { doc, past } = get()
    if (!doc) return
    const before = findBlock(doc, blockId)
    if (!before) return

    const translatableChanged =
      patch.translatable !== undefined && patch.translatable !== before.translatable

    const after: TranslationBlock = {
      ...before,
      ...patch,
      style: patch.style ? { ...before.style, ...patch.style } : before.style,
      is_edited: true,
      status:
        patch.status ??
        (patch.translatable === false
          ? 'skipped'
          : patch.translated_text !== undefined
            ? 'edited'
            : before.status),
    }

    const nextDoc = replaceBlock(doc, after)
    const nextPast = [...past, { blockId, before, after }].slice(-50)
    set({
      doc: nextDoc,
      past: nextPast,
      future: [],
      saveState: 'saving',
    })

    const persist = async () => {
      try {
        await api.updateBlock(doc.id, blockId, {
          translated_text: after.translated_text,
          bbox: after.bbox,
          style: after.style,
          translatable: after.translatable,
          status: after.status,
        })
        set((s) => ({
          saveState: 'saved',
          cleanBgVersion: translatableChanged ? s.cleanBgVersion + 1 : s.cleanBgVersion,
        }))
      } catch {
        set({ saveState: 'error' })
      }
    }

    const prevTimer = debounceTimers.get(blockId)
    if (prevTimer) clearTimeout(prevTimer)

    if (immediate || translatableChanged) {
      void persist()
    } else {
      debounceTimers.set(
        blockId,
        setTimeout(() => {
          debounceTimers.delete(blockId)
          void persist()
        }, 800),
      )
    }
  },

  undo: () => {
    const { doc, past, future } = get()
    if (!doc || past.length === 0) return
    const entry = past[past.length - 1]
    const nextDoc = replaceBlock(doc, entry.before)
    set({
      doc: nextDoc,
      past: past.slice(0, -1),
      future: [entry, ...future].slice(0, 50),
      saveState: 'saving',
    })
    void api
      .updateBlock(doc.id, entry.blockId, {
        translated_text: entry.before.translated_text,
        bbox: entry.before.bbox,
        style: entry.before.style,
        translatable: entry.before.translatable,
        status: entry.before.status,
      })
      .then(() => set({ saveState: 'saved' }))
      .catch(() => set({ saveState: 'error' }))
  },

  redo: () => {
    const { doc, past, future } = get()
    if (!doc || future.length === 0) return
    const entry = future[0]
    const nextDoc = replaceBlock(doc, entry.after)
    set({
      doc: nextDoc,
      past: [...past, entry].slice(-50),
      future: future.slice(1),
      saveState: 'saving',
    })
    void api
      .updateBlock(doc.id, entry.blockId, {
        translated_text: entry.after.translated_text,
        bbox: entry.after.bbox,
        style: entry.after.style,
        translatable: entry.after.translatable,
        status: entry.after.status,
      })
      .then(() => set({ saveState: 'saved' }))
      .catch(() => set({ saveState: 'error' }))
  },

  applyBlocksBatch: (updates, completedBlocks, totalBlocks) => {
    const { doc } = get()
    if (!doc || updates.length === 0) return

    const updateById = new Map<string, BlockTranslationUpdate>()
    const touchedPages = new Set<number>()
    let allHavePage = true
    for (const u of updates) {
      updateById.set(u.blockId, u)
      if (u.pageIndex !== undefined) {
        touchedPages.add(u.pageIndex)
      } else {
        allHavePage = false
      }
    }

    const pages = doc.pages.map((p) => {
      if (allHavePage && !touchedPages.has(p.page_index)) {
        return p
      }
      let pageChanged = false
      const nextBlocks = p.blocks.map((b) => {
        const u = updateById.get(b.id)
        if (!u) return b
        pageChanged = true
        return {
          ...b,
          translated_text: u.translatedText !== undefined ? u.translatedText : b.translated_text,
          status: u.status,
        }
      })
      return pageChanged ? { ...p, blocks: nextBlocks } : p
    })

    set({
      doc: {
        ...doc,
        pages,
        completed_blocks: completedBlocks !== undefined ? completedBlocks : doc.completed_blocks,
        total_blocks: totalBlocks !== undefined ? totalBlocks : doc.total_blocks,
      },
    })
  },

  applyBlockTranslated: (blockId, translatedText, status, completedBlocks, totalBlocks) => {
    get().applyBlocksBatch([{ blockId, translatedText, status }], completedBlocks, totalBlocks)
  },

  applyBlockFailed: (blockId) => {
    get().applyBlocksBatch([{ blockId, status: 'failed' }])
  },

  applyJobStatus: (status) => {
    const { doc } = get()
    if (!doc) return
    set({ doc: { ...doc, status } })
  },
}))
