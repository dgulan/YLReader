package parser

import (
	"encoding/json"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"
)

// RuleNodeKind 是列表节点的载体类型。
type RuleNodeKind int

const (
	// NodeHTML 表示 HTML 节点（CSS / XPath 切分产出）。
	NodeHTML RuleNodeKind = iota
	// NodeJSON 表示 JSON 节点（JSON 数组切分产出）。
	NodeJSON
	// NodeText 表示纯文本节点（JS / 正则切分产出）。
	NodeText
)

// RuleNode 是列表切分产出的一条结构化节点。字段不导出，统一走访问器，
// 避免与 html 包名冲突（docNode 而非 html）。
type RuleNode struct {
	Kind    RuleNodeKind
	docNode *html.Node      // NodeHTML 时有效
	raw     json.RawMessage // NodeJSON 时有效
	text    string          // NodeText 时有效
}

// Text 返回节点纯文本（对齐 M1a 提取逻辑）。
func (n RuleNode) Text() string {
	switch n.Kind {
	case NodeHTML:
		if n.docNode == nil {
			return ""
		}
		return strings.TrimSpace(htmlquery.InnerText(n.docNode))
	case NodeJSON:
		return string(n.raw)
	default:
		return n.text
	}
}

// OuterHTML 返回序列化 HTML 串（供需要整段 HTML 兜底求值的场景）。
func (n RuleNode) OuterHTML() string {
	if n.Kind != NodeHTML || n.docNode == nil {
		return n.Text()
	}
	var b strings.Builder
	if err := html.Render(&b, n.docNode); err != nil {
		return ""
	}
	out := b.String()
	// 表格片段（tr/td/th）脱离 table 上下文后会被 HTML 解析器丢弃；
	// 补最小上下文，保证节点内容可再次解析求值。
	switch strings.ToLower(n.docNode.Data) {
	case "tr":
		return "<table><tbody>" + out + "</tbody></table>"
	case "td", "th":
		return "<table><tbody><tr>" + out + "</tr></tbody></table>"
	}
	return out
}

// Attr 提取属性值；属性名大小写不敏感（脏 HTML 中 HREF/Href/href 混用）。
func (n RuleNode) Attr(key string) string {
	if n.Kind != NodeHTML || n.docNode == nil {
		return ""
	}
	for _, a := range n.docNode.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// Raw 返回底层字节串表征（HTML→OuterHTML，JSON→原始片段，文本→原串）。
func (n RuleNode) Raw() string {
	switch n.Kind {
	case NodeHTML:
		return n.OuterHTML()
	case NodeJSON:
		return string(n.raw)
	default:
		return n.text
	}
}
