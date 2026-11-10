// Package textnorm 提供书籍名 / 作者 / 章节标题的归一化，是业务写入的唯一主路径。
//
// 与 docs/schema.sql 中 normalize_book_key / books_fill_normalized_keys /
// chapter_title_key_fallback 的关系：
//   - SQL 版仅作运维回填与索引兜底；Go 版才是权威实现。
//   - Go 版在 SQL 版基础上增加 NFKC 与零宽字符清理，并额外支持中文数字章节号
//     （SQL 的 chapter_title_key_fallback 只识别阿拉伯数字）。此为 docs/architecture.md
//     §4.4 明确的有意设计差异，不得在审计中误判为不一致。
package textnorm

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// normalizeRe 与 schema.sql 的 normalize_book_key 字符类一致：
// 去空白（[:space:]）、ASCII 标点（[:punct:]）、以及 NFKC 不会折叠的 CJK 标点。
var normalizeRe = regexp.MustCompile(`[[:space:][:punct:]，。、；：？！“”‘’（）《》【】〈〉「」『』…—～·　]+`)

// authorPrefixRe 对齐 books_fill_normalized_keys 的前缀剥离：
// 行首「作者」或「著」+ 可选空白 + 可选冒号（ASCII 或全角）。
var authorPrefixRe = regexp.MustCompile(`^(?:作者|著)[[:space:]]*[:：]?`)

// authorSuffixRe 对齐 books_fill_normalized_keys 的后缀剥离（末尾「著」），
// 并额外覆盖括号形式（著）/（著）（SQL 版不处理括号，Go 版为超集）。
var authorSuffixRe = regexp.MustCompile(`[[:space:]]*(?:[（(][[:space:]]*著[[:space:]]*[)）]|著)[[:space:]]*$`)

// leadMarkerRe 去除章节号后的可选「章/节/回/话/卷/篇/集」标记（含前导空白）。
var leadMarkerRe = regexp.MustCompile(`^[[:space:]]*(?:章|节|回|话|卷|篇|集)?`)

// 中文数字：数字位与单位（十/百/千）。万/亿 超出「九千九百九十九」上限，识别但不解析。
var cnDigit = map[rune]int{
	'零': 0, '〇': 0,
	'一': 1, '二': 2, '两': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9,
}

var cnUnit = map[rune]int{
	'十': 10, '百': 100, '千': 1000,
}

var cnLargeUnit = map[rune]bool{
	'万': true, '亿': true,
}

// NormalizeBook 归一化书籍名：NFKC → 去零宽 → 小写 → 去空白与标点。
func NormalizeBook(name string) string {
	return normalizeKey(name)
}

// NormalizeAuthor 归一化作者名：先剥离「作者[:：]/著」前缀与末尾「著/(著)/（著）」
// 后缀，再走与 NormalizeBook 相同的归一化流程。
func NormalizeAuthor(author string) string {
	a := authorPrefixRe.ReplaceAllString(author, "")
	a = authorSuffixRe.ReplaceAllString(a, "")
	return normalizeKey(a)
}

// NormalizeChapter 归一化普通可读章节标题（is_volume=false 时由调用方调用）。
//
// 输出三段式：
//   - 有章节号且有标题：'n:<int>:<clean_title>'
//   - 有章节号无标题：'n:<int>'
//   - 无章节号：纯归一化文本
//
// 章节号支持阿拉伯数字（首位数字、最长 6 位）与中文数字（一 ~ 九千九百九十九）。
// 罗马数字不解析为数字，回退为纯标题。
func NormalizeChapter(title string) string {
	nfkc := norm.NFKC.String(title)
	runes := []rune(nfkc)
	num, end, ok := findFirstNumber(runes)
	if !ok {
		return normalizeKey(nfkc)
	}
	clean := cleanChapterTitle(runes, end)
	if clean == "" {
		return fmt.Sprintf("n:%d", num)
	}
	return fmt.Sprintf("n:%d:%s", num, clean)
}

// NormalizeChapterTitle 归一化卷名行（is_volume=true 时由调用方调用）。
// 只做纯文本归一化，不提取章节号、不产生 'n:' 前缀。
func NormalizeChapterTitle(title string) string {
	return normalizeKey(title)
}

// normalizeKey 执行统一归一化：NFKC → 去零宽字符 → 小写 → 去空白与标点。
func normalizeKey(s string) string {
	s = norm.NFKC.String(s)
	s = stripZeroWidth(s)
	s = strings.ToLower(s)
	return normalizeRe.ReplaceAllString(s, "")
}

// stripZeroWidth 移除零宽字符（零宽空格、连接符、方向标记、BOM 等）。
func stripZeroWidth(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isZeroWidth(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x200E, 0x200F, // ZWSP / ZWNJ / ZWJ / LRM / RLM
		0x202A, 0x202B, 0x202C, 0x202D, 0x202E, // 双向嵌入/覆盖
		0x2060, // WORD JOINER
		0xFEFF, // ZERO WIDTH NO-BREAK SPACE / BOM
		0x00AD, // SOFT HYPHEN
		0x180E: // MONGOLIAN VOWEL SEPARATOR
		return true
	}
	return false
}

// findFirstNumber 从字符串开头扫描，返回第一个章节号（阿拉伯或中文数字）的
// 数值与其结束位置（rune 下标）。无数字返回 ok=false。
//
// 中文数字（一 ~ 九千九百九十九）只有在合法章节序号上下文中才提取为章节号，
// 避免「一念永恒」「九州风云」「十字路口」「百年孤独」等普通词语被误识别。
func findFirstNumber(runes []rune) (num int, end int, ok bool) {
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if isArabicDigit(r) {
			j := i
			for j < len(runes) && isArabicDigit(runes[j]) && j-i < 6 {
				j++
			}
			v, err := strconv.Atoi(string(runes[i:j]))
			if err != nil {
				return 0, 0, false
			}
			return v, j, true
		}
		if isChineseNumeralChar(r) {
			j := i
			for j < len(runes) && isChineseNumeralChar(runes[j]) {
				j++
			}
			if v, ok2 := parseChineseNumeral(string(runes[i:j])); ok2 && isChapterNumeralContext(runes, i, j) {
				return v, j, true
			}
			// 含「万/亿」等超出上限，或非合法章节序号上下文：跳过该中文数字串。
			i = j - 1
		}
	}
	return 0, 0, false
}

// isChapterNumeralContext 判断中文数字串是否处于合法的章节序号上下文：
//
//	① 前紧邻「第/卷」序号前缀（如 第一百零八回、第一千章）；
//	② 后紧邻章节量词（章/节/回/话/卷/篇/集）或标点/空白分隔符（如 一百零八 风起、
//	   一、初入、十二章）；
//	③ 整个标题（去前后空白/标点）本身就是纯中文数字（如 第一百零八、十二）。
func isChapterNumeralContext(runes []rune, i, j int) bool {
	if i > 0 && (runes[i-1] == '第' || runes[i-1] == '卷') {
		return true
	}
	if j < len(runes) && (isChapterUnit(runes[j]) || isSeparator(runes[j])) {
		return true
	}
	return isPureNumeralTitle(runes, i, j)
}

// isChapterUnit 判断字符是否为章节量词。
func isChapterUnit(r rune) bool {
	switch r {
	case '章', '节', '回', '话', '卷', '篇', '集':
		return true
	}
	return false
}

// isSeparator 判断字符是否为归一化会移除的空白/标点分隔符（对齐 normalizeRe
// 字符类）。「～」属符号类（Sm）需单独处理。
func isSeparator(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsPunct(r) || r == '～'
}

// isPureNumeralTitle 判断数字串两侧是否只有空白/标点，即标题本身就是纯中文数字。
func isPureNumeralTitle(runes []rune, i, j int) bool {
	for k := 0; k < i; k++ {
		if !isSeparator(runes[k]) {
			return false
		}
	}
	for k := j; k < len(runes); k++ {
		if !isSeparator(runes[k]) {
			return false
		}
	}
	return true
}

// cleanChapterTitle 去掉章节号后的可选章节标记，再归一化剩余文本。
func cleanChapterTitle(runes []rune, numEnd int) string {
	rest := leadMarkerRe.ReplaceAllString(string(runes[numEnd:]), "")
	return normalizeKey(rest)
}

func isArabicDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isChineseNumeralChar(r rune) bool {
	if _, ok := cnDigit[r]; ok {
		return true
	}
	if _, ok := cnUnit[r]; ok {
		return true
	}
	if cnLargeUnit[r] {
		return true
	}
	return false
}

// parseChineseNumeral 解析中文数字（零 ~ 九千九百九十九）。含「万/亿」返回 ok=false。
func parseChineseNumeral(s string) (int, bool) {
	accumulated := 0
	number := 0
	for _, r := range s {
		if d, ok := cnDigit[r]; ok {
			number = d
			continue
		}
		if u, ok := cnUnit[r]; ok {
			if number == 0 {
				number = 1
			}
			accumulated += number * u
			number = 0
			continue
		}
		// 万/亿等大单位：超出支持范围
		return 0, false
	}
	return accumulated + number, true
}
