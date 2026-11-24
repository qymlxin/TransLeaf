import type {
  BlockStyle,
  DocumentIR,
  DocumentSummary,
  ExportMode,
  ProviderConfig,
  TranslationBlock,
} from '../types/document'

async function requestJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  if (!res.ok) {
    let msg = `请求失败 (HTTP ${res.status})`
    try {
      const data = await res.json()
      if (data?.error) msg = data.error
    } catch {
      // 忽略非 JSON 错误体
    }
    throw new Error(msg)
  }
  return res.json() as Promise<T>
}

export const api = {
  // 文档管理
  listDocuments: () => requestJSON<DocumentSummary[]>('/api/documents'),

  getDocumentIR: (docId: string) => requestJSON<DocumentIR>(`/api/documents/${docId}`),

  uploadDocument: async (file: File): Promise<DocumentSummary> => {
    const form = new FormData()
    form.append('file', file)
    return requestJSON<DocumentSummary>('/api/documents/upload', {
      method: 'POST',
      body: form,
    })
  },

  deleteDocument: (docId: string) =>
    requestJSON<{ deleted: boolean }>(`/api/documents/${docId}`, { method: 'DELETE' }),

  reparseDocument: (docId: string) =>
    requestJSON<{ status: string }>(`/api/documents/${docId}/reparse`, { method: 'POST' }),

  originalPdfUrl: (docId: string) => `/api/documents/${docId}/file`,

  cleanBgPdfUrl: (docId: string, version = 0) =>
    `/api/documents/${docId}/clean-bg?v=${version}`,

  thumbnailUrl: (docId: string) => `/api/documents/${docId}/thumbnail`,

  uploadThumbnail: async (docId: string, blob: Blob): Promise<void> => {
    await fetch(`/api/documents/${docId}/thumbnail`, {
      method: 'PUT',
      headers: { 'Content-Type': 'image/png' },
      body: blob,
    })
  },

  // 块级编辑与 AI 局部润色
  updateBlock: (
    docId: string,
    blockId: string,
    patch: Partial<{
      translated_text: string
      bbox: [number, number, number, number]
      style: BlockStyle
      translatable: boolean
      status: string
    }>,
  ) =>
    requestJSON<TranslationBlock>(`/api/documents/${docId}/blocks/${blockId}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    }),

  retranslateBlock: (
    docId: string,
    blockId: string,
    payload: { instruction: string; provider_id?: string; target_lang?: string },
  ) =>
    requestJSON<{ translated_text: string }>(
      `/api/documents/${docId}/blocks/${blockId}/retranslate`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      },
    ),

  // 翻译任务控制
  startTranslate: (
    docId: string,
    opts: {
      provider_id?: string
      source_lang?: string
      target_lang?: string
      style_preset?: string
      page_range?: string
      retranslate?: boolean
    },
  ) =>
    requestJSON<{ status: string }>(`/api/documents/${docId}/translate`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(opts),
    }),

  pauseTranslate: (docId: string) =>
    requestJSON<{ paused: boolean }>(`/api/documents/${docId}/translate/pause`, {
      method: 'POST',
    }),

  // 高保真导出
  exportDocument: async (docId: string, mode: ExportMode, filename: string): Promise<void> => {
    const res = await fetch(`/api/documents/${docId}/export`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    })
    if (!res.ok) {
      let msg = `导出失败 (HTTP ${res.status})`
      try {
        const d = await res.json()
        if (d?.error) msg = d.error
      } catch {
        // 忽略
      }
      throw new Error(msg)
    }
    const blob = await res.blob()
    const base = filename.replace(/\.pdf$/i, '')
    const suffix = mode === 'translated' ? '_translated.pdf' : '_bilingual.pdf'
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = base + suffix
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  },

  // 模型配置
  listProviders: () => requestJSON<ProviderConfig[]>('/api/providers'),

  createProvider: (p: Omit<ProviderConfig, 'id' | 'created_at'>) =>
    requestJSON<ProviderConfig>('/api/providers', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(p),
    }),

  updateProvider: (id: string, p: Partial<ProviderConfig>) =>
    requestJSON<ProviderConfig>(`/api/providers/${id}`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(p),
    }),

  deleteProvider: (id: string) =>
    requestJSON<{ deleted: boolean }>(`/api/providers/${id}`, { method: 'DELETE' }),

  testProvider: (p: Partial<ProviderConfig>) =>
    requestJSON<{ ok: boolean; latency_ms: number; error?: string }>('/api/providers/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(p),
    }),
}
