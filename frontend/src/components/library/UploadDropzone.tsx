import { useRef, useState } from 'react'
import { FileUp, Loader2 } from 'lucide-react'

interface Props {
  uploading: boolean
  onUpload: (file: File) => void
}

export function UploadDropzone({ uploading, onUpload }: Props) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [dragging, setDragging] = useState(false)

  const handleFiles = (files: FileList | null) => {
    if (!files || files.length === 0 || uploading) return
    onUpload(files[0])
  }

  return (
    <div
      onClick={() => !uploading && inputRef.current?.click()}
      onDragOver={(e) => {
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        e.preventDefault()
        setDragging(false)
        handleFiles(e.dataTransfer.files)
      }}
      className={`group flex cursor-pointer flex-col items-center justify-center rounded-2xl border-2 border-dashed px-6 py-10 text-center transition ${
        dragging
          ? 'border-indigo-500 bg-indigo-50/70'
          : 'border-slate-300 bg-white hover:border-indigo-400 hover:bg-slate-50/80'
      }`}
    >
      <input
        ref={inputRef}
        type="file"
        accept=".pdf,application/pdf"
        className="hidden"
        onChange={(e) => {
          handleFiles(e.target.files)
          e.target.value = ''
        }}
      />
      <div className="mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-indigo-50 text-indigo-600 transition group-hover:scale-105">
        {uploading ? (
          <Loader2 className="h-6 w-6 animate-spin" />
        ) : (
          <FileUp className="h-6 w-6" />
        )}
      </div>
      <p className="text-base font-semibold text-slate-800">
        {uploading ? '正在上传并启动纯 Go 引擎解析版面...' : '拖拽 PDF 文档到此处，或点击选择文件'}
      </p>
      <p className="mt-1 text-xs text-slate-500">
        纯 Go 自研 PDF 引擎指令级去字留图 · 支持双栏论文、图表与表格线 100% 保留 · 最大 100MB
      </p>
    </div>
  )
}
