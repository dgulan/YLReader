package service

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"

	"yreader/backend/internal/repository"
)

// TXT 分章正则：允许行首缩进（含全角空格）。
const (
	txtLead = `[\s\x{3000}]*`
	txtNum  = `[零一二三四五六七八九十百千万\d]+`
)

var (
	reZhChapter = regexp.MustCompile(`(?m)^` + txtLead + `第` + txtNum + `章[^\n]*$`)
	reZhSection = regexp.MustCompile(`(?m)^` + txtLead + `第` + txtNum + `节[^\n]*$`)
	reVolume    = regexp.MustCompile(`(?m)^` + txtLead + `(?:第` + txtNum + `卷|卷[零一二三四五六七八九十百千\d]+)[^\n]*$`)
	reEnChapter = regexp.MustCompile(`(?m)^` + txtLead + `(?:Chapter|CHAPTER|Chap\.?)\s+[\dIVXivx]+[^\n]*$`)
	reNumTitle  = regexp.MustCompile(`(?m)^` + txtLead + `\d+[\.、\s][^\n]{0,30}$`)
)

// txtSingleChapterMaxRunes 无标题文本的切块阈值。
const txtSingleChapterMaxRunes = 50000

// decodeTXT 按 BOM → UTF-16 → UTF-8 → GBK → Big5 → ISO-8859-1 顺序解码。
func decodeTXT(raw []byte) string {
	switch {
	case len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF:
		return string(raw[3:])
	case len(raw) >= 2 && raw[0] == 0xFF && raw[1] == 0xFE:
		return decodeUTF16(raw[2:], unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM))
	case len(raw) >= 2 && raw[0] == 0xFE && raw[1] == 0xFF:
		return decodeUTF16(raw[2:], unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM))
	}

	// 直接对整个缓冲区做 UTF-8 判定：按字节切片抽样可能截断多字节序列，
	// 导致合法 UTF-8 被误判并降级成 GBK（3000 章样本曾因此只切出 4 块）。
	if utf8.Valid(raw) {
		return string(raw)
	}
	// GBK 与 Big5 的字节区间存在重叠：同一段字节往往能被两者「成功」解码，
	// 但只有其中一种还原出真实文字。这里按「分章标题命中数」打分择优，
	// 平票时保持规范顺序（GBK 优先）。
	gbkText, gbkOK := decodeIfValid(raw, simplifiedchinese.GBK.NewDecoder())
	big5Text, big5OK := decodeIfValid(raw, traditionalchinese.Big5.NewDecoder())
	switch {
	case gbkOK && big5OK:
		if chapterScore(big5Text) > chapterScore(gbkText) {
			return big5Text
		}
		return gbkText
	case gbkOK:
		return gbkText
	case big5OK:
		return big5Text
	}
	out, _ := charmap.ISO8859_1.NewDecoder().Bytes(raw)
	return string(out)
}

// decodeIfValid 用指定解码器解码；解码失败或结果不是合法 UTF-8 时返回 false。
func decodeIfValid(raw []byte, dec *encoding.Decoder) (string, bool) {
	out, err := dec.Bytes(raw)
	if err != nil || !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

// chapterScore 统计文本中分章标题的命中数，用于在 GBK/Big5 之间择优。
func chapterScore(s string) int {
	return len(reZhChapter.FindAllStringIndex(s, -1)) +
		len(reZhSection.FindAllStringIndex(s, -1)) +
		len(reVolume.FindAllStringIndex(s, -1))
}

func decodeUTF16(b []byte, enc encoding.Encoding) string {
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

// normalizeText 统一换行并压缩多余空行。
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t\u3000")
	}
	s = strings.Join(lines, "\n")
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(s)
}

type txtSplitPattern struct {
	name string
	re   *regexp.Regexp
}

// parseTXTRaw 把已解码的纯文本切成章节。
func parseTXTRaw(text, bookTitle string) (*parsedBook, []string) {
	var warnings []string
	pb := &parsedBook{Title: bookTitle}

	// 投票顺序即平票优先级：中文章节 > 卷章结构 > 中文小节 > 英文章节 > 数字标题。
	patterns := []txtSplitPattern{
		{"中文章节", reZhChapter},
		{"卷章结构", reVolume},
		{"中文小节", reZhSection},
		{"英文章节", reEnChapter},
		{"数字标题", reNumTitle},
	}
	bestIdx, bestCount := -1, 0
	for i, p := range patterns {
		if n := len(p.re.FindAllStringIndex(text, -1)); n > bestCount {
			bestIdx, bestCount = i, n
		}
	}

	if bestIdx < 0 || bestCount <= 1 {
		runes := []rune(normalizeText(text))
		if len(runes) <= txtSingleChapterMaxRunes {
			warnings = append(warnings, "未识别到章节标题，整本作为单章")
			title := strings.TrimSpace(bookTitle)
			if title == "" {
				title = "正文"
			}
			pb.Chapters = []parsedChapter{{Title: title, Content: string(runes)}}
			return pb, warnings
		}
		warnings = append(warnings, fmt.Sprintf("未识别到章节标题，按 %d 字符切块", txtSingleChapterMaxRunes))
		for i := 0; i < len(runes); i += txtSingleChapterMaxRunes {
			end := i + txtSingleChapterMaxRunes
			if end > len(runes) {
				end = len(runes)
			}
			pb.Chapters = append(pb.Chapters, parsedChapter{
				Title:   fmt.Sprintf("第 %d 部分", len(pb.Chapters)+1),
				Content: string(runes[i:end]),
			})
		}
		return pb, warnings
	}

	re := patterns[bestIdx].re
	locs := re.FindAllStringIndex(text, -1)
	for i, loc := range locs {
		title := strings.TrimSpace(text[loc[0]:loc[1]])
		start := loc[1]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		pb.Chapters = append(pb.Chapters, parsedChapter{
			Title:    title,
			IsVolume: re == reVolume,
			Content:  normalizeText(text[start:end]),
		})
	}
	return pb, warnings
}

// parseTXT 解码并分章。
func (s *ImportService) parseTXT(file *os.File, size int64, bookTitle string) (*parsedBook, []string, error) {
	raw, err := io.ReadAll(io.LimitReader(file, size+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read txt: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil, fmt.Errorf("%w: empty file", repository.ErrInvalidArgument)
	}
	pb, warnings := parseTXTRaw(decodeTXT(raw), bookTitle)
	return pb, warnings, nil
}
