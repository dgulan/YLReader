package parser

import (
	"fmt"
	"slices"
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

// ContentPaginationLinks returns explicit next-page links for the service's URL checks.
func (n RuleNode) ContentPaginationLinks() []string {
	if n.Kind != NodeHTML || n.docNode == nil {
		return nil
	}
	var links []string
	goquery.NewDocumentFromNode(n.docNode).
		Find("a[rel~=next][href], .prenext a[href], .bottem2 a[href], .page_chapter a[href]").
		Each(func(_ int, link *goquery.Selection) {
			label := strings.TrimSpace(link.Text())
			if label == "下一章" || label == "下章" {
				return
			}
			rel := strings.Fields(strings.ToLower(link.AttrOr("rel", "")))
			if label == "下一页" || label == "下页" || slices.Contains(rel, "next") {
				links = append(links, link.AttrOr("href", ""))
			}
		})
	return links
}
