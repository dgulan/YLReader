package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/uuid"
	"yreader/backend/internal/repository"
	"yreader/backend/internal/service"
)

// maxLocalImportBytes 单文件导入上限（100 MiB）。
const maxLocalImportBytes = 100 << 20

type LocalImporter interface {
	ImportBook(context.Context, uuid.UUID, *os.File, int64, string, string, string, *string, *string) (*service.ImportBookResult, error)
}

// ImportLocalBook 导入本地 EPUB / TXT（原始字节流 + query 元数据）。
func (s *Server) ImportLocalBook(w http.ResponseWriter, r *http.Request, params ImportLocalBookParams) {
	filename := sanitizeFilename(params.Filename)
	if filename == "" {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxLocalImportBytes)
	tmp, err := os.CreateTemp("", "yreader_upload_*.tmp")
	if err != nil {
		writeError(w, r, err)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErrorCode(w, r, http.StatusRequestEntityTooLarge, "payload_too_large")
			return
		}
		writeError(w, r, err)
		return
	}
	fileHash := hex.EncodeToString(hasher.Sum(nil))

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeError(w, r, err)
		return
	}
	fileType, ok := detectFileType(tmp, size, filename)
	if !ok {
		writeErrorCode(w, r, http.StatusBadRequest, "validation_error")
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		writeError(w, r, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.importTimeout)
	defer cancel()

	result, err := s.Import.ImportBook(ctx, userIDFrom(r.Context()), tmp, size,
		fileHash, filename, fileType, params.Title, params.Author)
	if err != nil {
		if r.Context().Err() == nil && (errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)) {
			w.Header().Set("Retry-After", "10")
			writeErrorCode(w, r, http.StatusGatewayTimeout, "import_timeout")
			return
		}
		if errors.Is(err, service.ErrDRMProtected) {
			writeJSON(w, http.StatusUnprocessableEntity, ErrorResponse{
				Error: Error{
					Code:    ErrorCodeUnsupportedRule,
					Message: "DRM protected books are not supported",
					Details: &map[string]interface{}{"feature": "drm_protected"},
				},
			})
			return
		}
		writeError(w, r, err)
		return
	}

	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, toAPIBookImportResult(result))
}

// sanitizeFilename 只保留基名并拒绝路径穿越与非法字符。
func sanitizeFilename(raw string) string {
	if raw == "" {
		return ""
	}
	s := strings.ReplaceAll(raw, "\\", "/")
	s = path.Base(s)
	if s == "" || s == "." || s == ".." || strings.ContainsAny(s, "/\\\x00") {
		return ""
	}
	return s
}

// detectFileType 严格扩展名白名单 + 魔数优先 + UTF-16 BOM + 512B 文本嗅探。
func detectFileType(f *os.File, size int64, filename string) (string, bool) {
	if size < 2 {
		return "", false
	}
	lower := strings.ToLower(filename)
	isTxtExt := strings.HasSuffix(lower, ".txt")
	isEpubExt := strings.HasSuffix(lower, ".epub")
	if !isTxtExt && !isEpubExt {
		return "", false
	}

	head := make([]byte, 4)
	nr, err := io.ReadFull(f, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		return "", false
	}

	// 1. 白名单内命中 PK 魔数 → EPUB
	if nr >= 4 && head[0] == 'P' && head[1] == 'K' && head[2] == 3 && head[3] == 4 {
		return "epub", true
	}
	// 2. UTF-16 BOM → TXT
	if nr >= 2 && ((head[0] == 0xFF && head[1] == 0xFE) || (head[0] == 0xFE && head[1] == 0xFF)) {
		return "txt", true
	}
	// 3. 512B NUL 嗅探：无 NUL 视为文本
	sniffSize := 512
	if int64(sniffSize) > size {
		sniffSize = int(size)
	}
	sniff := make([]byte, sniffSize)
	_, _ = f.Seek(0, io.SeekStart)
	n, _ := io.ReadFull(f, sniff)
	if bytes.Contains(sniff[:n], []byte{0x00}) {
		return "", false
	}
	return "txt", true
}

// toAPIBookImportResult 映射导入结果；warnings 恒为非 nil 数组。
func toAPIBookImportResult(r *service.ImportBookResult) BookImportResult {
	warnings := r.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return BookImportResult{
		Book:         toAPIBookForImport(r.Book),
		ChapterCount: r.ChapterCount,
		FileType:     BookImportResultFileType(strings.ToLower(r.FileType)),
		FileHash:     r.FileHash,
		Warnings:     &warnings,
	}
}

// toAPIBookForImport 导入响应中的书籍不携带进度（前端按需 GET /progress）。
func toAPIBookForImport(b *repository.Book) Book {
	apiBook := toAPIBook(b, nil)
	apiBook.Progress = nil
	return apiBook
}
