package parser

import (
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// NewHTMLDocumentNode 解析整页 HTML，并包装为可直接交给 EvalField 消费的根节点。
// 用于正文抓取场景：整页只有一个文档节点，字段规则（如 @css:.content@html）在根节点上求值。
func NewHTMLDocumentNode(body string) (RuleNode, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return RuleNode{}, err
	}
	if len(doc.Nodes) == 0 {
		return RuleNode{}, fmt.Errorf("empty html document")
	}
	return RuleNode{Kind: NodeHTML, docNode: doc.Nodes[0]}, nil
}
