package multimodalmcp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"loadout/core/mcpkit"
)

// understandDocument 实现 understand_document 工具：文档/PDF 理解（走视觉能力，
// 见火山「文档理解」文档——模型把 PDF 分页处理成多图后理解文本/图片等内容）。
//   - args.document 文档资源（url / data URI / file:// 本地路径，三态）。
//   - args.prompt   可选自由描述提示；为空时用默认文档理解提示。
//
// 走 responses 协议（chat 协议不支持 input_file），分三种传入方式：
//   - 本地文件 ≤50MB → base64 data URI → input_file.file_data + filename；
//   - 本地文件 >50MB → Files API 上传（≤512MB）拿 file_id → input_file.file_id（推荐）；
//   - 公网 URL → input_file.file_url。
func (s *Service) understandDocument(ctx context.Context, args map[string]any) (*mcpkit.ToolResult, error) {
	if err := s.checkEndpointEnabled(); err != nil {
		return nil, err
	}
	docRef := strArg(args, "document")
	if strings.TrimSpace(docRef) == "" {
		return nil, errors.New("multimodal-mcp: understand_document 缺少 document 参数")
	}
	model, err := s.modelFor(ToolDocument)
	if err != nil {
		return nil, err
	}
	prompt := strArg(args, "prompt")
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultDocumentPrompt
	}

	// 资源三态解析：本地文件 ≤50MB 转 base64 data URI、>50MB 走 Files API 拿 file_id；
	// URL / data URI 原样返回。
	mime := mimeByExt(strings.TrimPrefix(docRef, "file://"))
	url, fileID, err := s.resolveResource(ctx, docRef, mime, documentSizeLimit)
	if err != nil {
		return nil, err
	}
	// 本地文件路径 → 文件名（base64 传入时 responses 的 input_file 需要 filename）。
	filename := ""
	if classifySource(docRef) == "file" {
		filename = filepath.Base(strings.TrimPrefix(docRef, "file://"))
	}

	blocks := []map[string]any{
		inputFileBlockResponses(url, fileID, filename),
		inputTextBlockResponses(prompt),
	}
	return s.runRecognition(ctx, "understand_document", "文档理解", model, map[string]any{
		"tool":   "understand_document",
		"source": classifySource(docRef),
		"format": mime,
	}, func() recognitionResult {
		text, reqLog, channel, err := s.callResponses(ctx, model, blocks, "", callOpts{})
		return recognitionResult{text: text, reqLog: reqLog, channel: channel, err: err}
	})
}
