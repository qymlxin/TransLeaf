# TranslationAssistant（AI 高保真文档翻译助手）

一款本地优先、Web 端交互、格式高保真的 AI 文档翻译与校对工作台。首期聚焦 **PDF 文档智能解析、指令级无损擦除、流式并发翻译、双底图实时对照预览、所见即所得在线编辑与高保真导出**。

## 核心特点
- **纯 Go 自研 PDF 核心引擎（`backend/pkg/pdfengine`）**：零 CGo、零 Python 依赖，完整自研 PDF 对象/XRef 解析、`ToUnicode` CMap 状态机、Content Stream 图形状态机、指令级无损擦除、CIDFont 中文字体嵌入与混排自适应排版。
- **全栈 100% 商业友好开源协议**：严格杜绝 GPL / AGPL / 商业授权库，所有依赖均为 MIT / Apache-2.0 / BSD-3-Clause / SIL OFL-1.1。
- **双底图无损预览（`clean_bg.pdf`）**：解析即生成去除可译文字的纯净底图，Web 编辑态与最终导出 PDF 达到 100% 视觉一致，完美保留底层图片、表格边框与渐变背景。

## 快速启动

### 1. 启动 Go 后端
```bash
cd backend
go run ./cmd/server
```
后端默认监听 `http://127.0.0.1:8080`。

### 2. 启动 React 前端
```bash
cd frontend
npm install
npm run dev
```
前端默认监听 `http://localhost:5173`，自动将 `/api` 代理至 Go 后端。

## 规格文档（SDD）
- [需求规格说明书](docs/specs/requirements.md)
- [系统架构与详细设计](docs/specs/design.md)
- [执行任务与验收清单](docs/specs/tasks.md)
