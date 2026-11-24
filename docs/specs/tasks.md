# 执行任务清单与验收对照表（Tasks & Verification Checklist）

- **项目名称**：TransLeaf（AI 高保真文档翻译助手）
- **版本**：v2.0（纯 Go 自研 PDF 引擎 + 全栈商业友好开源协议版）
- **关联文档**：
  - [requirements.md](file:///Users/qymlxin/.gemini/antigravity/brain/e8d631fc-d771-4fd6-99fb-69af986d1561/requirements.md)
  - [design.md](file:///Users/qymlxin/.gemini/antigravity/brain/e8d631fc-d771-4fd6-99fb-69af986d1561/design.md)

---

## M0：工程脚手架与合规基础设施

- [x] **T0.1 初始化仓库目录与 SDD 文档沉淀**
- [x] **T0.2 初始化纯 Go 后端工程（零 CGo，`CGO_ENABLED=0 go build` 通过）**
- [x] **T0.3 准备开源中文字体路径与自动探测回退机制**（优先加载 `backend/assets/fonts/NotoSansSC-Regular.ttf`，开发机自动发现系统兼容 TTF）
- [x] **T0.4 初始化 React 19 + TypeScript + Vite 前端工程（`npm run build` 零错误通过）**

## M1：自研纯 Go PDF 引擎 — 解析与状态机（映射 `REQ-02`）

- [x] **T1.1 实现 PDF 词法、对象模型与 XRef/ObjStm/流解析与重写器（`pkg/pdfengine/object`）**
- [x] **T1.2 实现字体编码与 `ToUnicode` CMap 状态机（`pkg/pdfengine/font`）**
- [x] **T1.3 实现 Content Stream 解释器与图形状态机（`pkg/pdfengine/content`）**
- [x] **T1.4 实现版面段落合并、双栏排序与非可译过滤（`layout_merge.go`）**
- [x] **T1.5 编写 M1 解析内核单元测试（`go test -race ./pkg/pdfengine/...` 通过）**

## M2：自研纯 Go PDF 引擎 — 无损擦除与高保真导出（映射 `REQ-02`, `REQ-07`）

- [x] **T2.1 实现指令级无损文字擦除器（`pkg/pdfengine/redactor`），100% 保留图片与矢量线条**
- [x] **T2.2 实现中文字体 CIDFontType2 嵌入与精确 `ToUnicode` CMap 模块（`pkg/pdfengine/subsetter/cidfont.go`）**
- [x] **T2.3 实现中英混排折行与字号二分自适应排版器（`pkg/pdfengine/layout`）**
- [x] **T2.4 实现纯译文、交替页双语与左右并排双语导出门面（`pkg/pdfengine/engine.go`）**
- [ ] **T2.5 [M2b 后续优化] 实现 TrueType 字体子集裁剪器（`ttf_subset.go`）进一步压降导出体积**
- [x] **T2.6 编写 M2 擦除与导出端到端单测（已通过反读中文与双语结构校验）**

## M3：Go 业务后端、数据库与文档管理（映射 `REQ-01`）

- [x] **T3.1 实现 SQLite（纯 Go `modernc.org/sqlite`）初始化与 Repository 层（`internal/store`）**
- [x] **T3.2 定义插件化文档适配器接口与 `PDFAdapter`（`internal/adapter`）**
- [x] **T3.3 实现文档管理、纯净底图、缩略图缓存、块级编辑与导出 API（`internal/api/server.go`）**

## M4：AI 模型配置与 Goroutine 并发流式翻译（映射 `REQ-03`, `REQ-04`）

- [x] **T4.1 实现自封装 OpenAI 兼容客户端（`internal/llm/client.go`）**
- [x] **T4.2 实现模型配置 CRUD、连通性探针与 API Key 脱敏接口**
- [x] **T4.3 实现并发批处理翻译调度、暂停/断点续译与 SSE 事件中心（`internal/service/translator.go`）**
- [x] **T4.4 编写后端全链路 API 单元测试（`go test -race ./...` 零数据竞争通过）**

## M5：Web 端双底图保真预览与所见即所得编辑（映射 `REQ-01`, `REQ-03`, `REQ-05`, `REQ-06`）

- [x] **T5.1 实现首页文档库、拖拽上传与模型配置弹窗（`HomePage.tsx`、`ModelSettingsModal.tsx`）**
- [x] **T5.2 实现工作台容器、Zustand Store（50 步 Undo/Redo + 800ms 防抖）与 SSE 实时同步**
- [x] **T5.3 实现双底图同步预览画布与透明译文编辑层（`DualPaneViewer.tsx`、`TranslationOverlay.tsx`）**
- [x] **T5.4 实现右侧属性检查器（含 AI 局部润色）与 CAT 段落校对列表（`BlockInspector.tsx`、`SegmentListView.tsx`）**

## M6：全链路集成测试与端到端验收

- [x] **T6.1 自动化构建与测试门槛**
  - [x] `cd backend && go test -race ./...` 全部通过
  - [x] `cd backend && CGO_ENABLED=0 go build -o bin/server ./cmd/server` 零 CGo 单二进制编译成功
  - [x] `cd frontend && npm run build` TypeScript 类型检查与生产构建零错误
- [ ] **T6.2 端到端真实样本人工验收（待您启动后使用真实 PDF 实测）**
  - [ ] 场景 1（单栏带图/表格 PDF）：验证 `clean_bg.pdf` 保留图文、流式翻译与纯译文导出
  - [ ] 场景 2（双栏英文学术论文）：验证双栏顺序、原地双击编辑、拖拽改框大小与双语导出
  - [ ] 场景 3（异常边界）：验证加密 PDF / 纯图片扫描 PDF 友好中文提示

## M7：Phase 2 / Phase 3 演进 Backlog（全合规扩展）

- [ ] **B1（P1）术语库（Glossary）管理与 Prompt 自动注入**
- [ ] **B2（P1）扫描件 PDF OCR 扩展（Apache-2.0 PaddleOCR 或 VLM 视觉模型 API）**
- [ ] **B3（P2）纯 Go 自研 OOXML（`.docx` / `.pptx`）保真解析与导出适配器**
- [ ] **B4（P2）翻译记忆库（TM）自动沉淀与相似句段复用**
