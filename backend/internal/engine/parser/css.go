package parser

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/net/html"
)

// evalCSS 用 goquery 求值 CSS 规则（默认模式），支持 && / || / %% 组合与
// selector@getter 提取（getter：text/textNodes/ownText/html/all/属性名）。
// 选择器支持 Legado 简写：class./tag./id./text./children 及单索引 .N / .-N / [N]。
func evalCSS(rule, content string) ([]string, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return []string{doc.Text()}, nil
	}
	segs, op, err := splitRule(rule, "&&", "||", "%%")
	if err != nil {
		return nil, err
	}
	var results [][]string
	for _, seg := range segs {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		temp := cssSegment(doc.Selection, seg)
		if len(temp) > 0 {
			results = append(results, temp)
			if op == "||" {
				break
			}
		}
	}
	return combine(results, op), nil
}

// cssSegment 求值单个 CSS 段落：按 @ 链式选择器，末段为 getter。
// 无 @ 时按 Legado 语义默认取文本。
func cssSegment(root *goquery.Selection, seg string) []string {
	parts := strings.Split(seg, "@")
	if len(parts) == 1 {
		return getResultLast(selectElements(root, strings.TrimSpace(seg)), "text")
	}
	sel := root
	for _, p := range parts[:len(parts)-1] {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		sel = selectElements(sel, p)
	}
	return getResultLast(sel, strings.TrimSpace(parts[len(parts)-1]))
}

var (
	trailingDotIndex     = regexp.MustCompile(`\.(-?\d+)$`)
	trailingBracketIndex = regexp.MustCompile(`\[(-?\d+)\]$`)
	exclusionSuffix      = regexp.MustCompile(`!\d+$`)
	starSuffix           = regexp.MustCompile(`\[\*\]`)
)

// selectElements 应用 Legado 选择器简写与单索引，返回筛选后的 Selection。
// 无索引时返回全部命中；索引越界返回空选择集。
func selectElements(root *goquery.Selection, selector string) *goquery.Selection {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return root
	}
	base := selector
	// Legado 排除语法 `!N`：M1c 仅剥离（不参与筛选），保证列表切分可用。
	base = exclusionSuffix.ReplaceAllString(strings.TrimSpace(base), "")
	// `[*]` 在部分书源中表示「全部元素」，等价于无索引。
	base = starSuffix.ReplaceAllString(base, "")
	var index *int
	if m := trailingDotIndex.FindStringSubmatch(base); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			index = &n
		}
		base = base[:len(base)-len(m[0])]
	} else if m := trailingBracketIndex.FindStringSubmatch(base); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			index = &n
		}
		base = base[:len(base)-len(m[0])]
	}

	base = strings.TrimSpace(base)
	var sel *goquery.Selection
	if base == "" {
		sel = root.Children()
	} else {
		sel = findBySelector(root, base)
	}
	if index == nil {
		return sel
	}
	idx := *index
	if idx < 0 {
		idx += len(sel.Nodes)
	}
	if idx < 0 || idx >= len(sel.Nodes) {
		return sel.Slice(0, 0)
	}
	return sel.Eq(idx)
}

// findBySelector 把 Legado 选择器简写转成标准 CSS 并查找后代。
// 对裸标识符（如 `book_name`，既非合法标签也无 class 前缀）回退为 class 选择器，
// 兼容线上大量以裸类名书写规则的 Legado 书源。
func findBySelector(root *goquery.Selection, sel string) *goquery.Selection {
	if sel == "children" {
		return root.Children()
	}
	if i := strings.IndexByte(sel, '.'); i > 0 {
		switch sel[:i] {
		case "class":
			// `class.a b` 在 Legado 中表示同一元素同时具备多个类。
			return root.Find("." + strings.Join(strings.Fields(sel[i+1:]), "."))
		case "tag":
			return root.Find(sel[i+1:])
		case "id":
			return root.Find("#" + sel[i+1:])
		case "text":
			return root.Find(":contains(" + sel[i+1:] + ")")
		}
	}
	found := root.Find(sel)
	if found.Length() == 0 && bareIdent.MatchString(sel) && !htmlTags[strings.ToLower(sel)] {
		if alt := root.Find("." + sel); alt.Length() > 0 {
			return alt
		}
	}
	return found
}

var (
	bareIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
	htmlTags  = map[string]bool{
		"a": true, "abbr": true, "article": true, "aside": true, "b": true, "big": true,
		"blockquote": true, "body": true, "br": true, "button": true, "center": true,
		"code": true, "dd": true, "div": true, "dl": true, "dt": true, "em": true,
		"figure": true, "font": true, "footer": true, "form": true, "h1": true, "h2": true,
		"h3": true, "h4": true, "h5": true, "h6": true, "head": true, "header": true,
		"hr": true, "html": true, "i": true, "iframe": true, "img": true, "input": true,
		"label": true, "li": true, "main": true, "nav": true, "ol": true, "option": true,
		"p": true, "pre": true, "s": true, "section": true, "select": true, "small": true,
		"span": true, "strong": true, "sub": true, "sup": true, "table": true, "tbody": true,
		"td": true, "textarea": true, "tfoot": true, "th": true, "thead": true, "title": true,
		"tr": true, "u": true, "ul": true,
	}
)

// getResultLast 按 getter 从选择集中提取文本（对齐 AnalyzeByJSoup.getResultLast）。
func getResultLast(sel *goquery.Selection, getter string) []string {
	switch getter {
	case "text":
		var out []string
		sel.Each(func(_ int, s *goquery.Selection) {
			if t := s.Text(); t != "" {
				out = append(out, t)
			}
		})
		return out
	case "textNodes":
		var out []string
		sel.Each(func(_ int, s *goquery.Selection) {
			var parts []string
			s.Contents().Each(func(_ int, n *goquery.Selection) {
				if node := n.Get(0); node != nil && node.Type == html.TextNode {
					if t := strings.TrimSpace(node.Data); t != "" {
						parts = append(parts, t)
					}
				}
			})
			if len(parts) > 0 {
				out = append(out, strings.Join(parts, "\n"))
			}
		})
		return out
	case "ownText":
		var out []string
		sel.Each(func(_ int, s *goquery.Selection) {
			if t := ownText(s); t != "" {
				out = append(out, t)
			}
		})
		return out
	case "html":
		sel = sel.Clone()
		sel.Find("script, style").Remove()
		var out []string
		sel.Each(func(_ int, s *goquery.Selection) {
			if h, err := goquery.OuterHtml(s); err == nil && h != "" {
				out = append(out, h)
			}
		})
		return out
	case "all":
		var out []string
		sel.Each(func(_ int, s *goquery.Selection) {
			if h, err := goquery.OuterHtml(s); err == nil && h != "" {
				out = append(out, h)
			}
		})
		return out
	default:
		var out []string
		seen := map[string]bool{}
		sel.Each(func(_ int, s *goquery.Selection) {
			if v, ok := s.Attr(getter); ok && v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		})
		return out
	}
}

// ownText 返回元素的直接文本节点内容。
func ownText(s *goquery.Selection) string {
	var b strings.Builder
	s.Contents().Each(func(_ int, n *goquery.Selection) {
		if node := n.Get(0); node != nil && node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
	})
	return strings.TrimSpace(b.String())
}

// combine 按操作符合并多段结果：|| 首个非空（已 break）、%% 交错、&& 顺序追加。
func combine(results [][]string, op string) []string {
	if len(results) == 0 {
		return []string{}
	}
	if op == "%%" {
		out := make([]string, 0)
		for i := 0; i < len(results[0]); i++ {
			for _, r := range results {
				if i < len(r) {
					out = append(out, r[i])
				}
			}
		}
		return out
	}
	out := make([]string, 0)
	for _, r := range results {
		out = append(out, r...)
	}
	return out
}
