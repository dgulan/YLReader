package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"strings"

	"golang.org/x/net/html"

	"yreader/backend/internal/repository"
)

const (
	epubMaxEntryBytes = 8 << 20   // 单条目解压上限 8 MiB
	epubMaxTotalBytes = 128 << 20 // 整本解压累计上限 128 MiB
	epubMaxTOCEntries = 5000      // 最大目录条目数
)

var errEPUBEntryNotFound = errors.New("epub entry not found")

type epubReadBudget struct {
	remaining int64
}

// ---- EPUB 元数据容器 ----

type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubManifestItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type epubPackage struct {
	Metadata struct {
		Title   string   `xml:"title"`
		Creator []string `xml:"creator"`
		Meta    []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []epubManifestItem `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type ncxPoint struct {
	Label  string     `xml:"navLabel>text"`
	Src    string     `xml:"content>src,attr"`
	Points []ncxPoint `xml:"navPoint"`
}

type epubNCX struct {
	NavMap struct {
		Points []ncxPoint `xml:"navPoint"`
	} `xml:"navMap"`
}

// tocEntry 是打平后的目录项。
type tocEntry struct {
	Title    string
	Href     string
	HasChild bool
}

// readZipByName 读取 zip 内条目（容忍大小写与 %20 转义差异），受单条目和整本解压上限保护。
func readZipByName(ctx context.Context, zr *zip.Reader, name string, budget *epubReadBudget) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidates := []string{name, strings.ToLower(name)}
	if unesc, err := url.PathUnescape(name); err == nil {
		candidates = append(candidates, unesc, strings.ToLower(unesc))
	}
	for _, c := range candidates {
		for _, f := range zr.File {
			if strings.EqualFold(f.Name, c) {
				return readZipFileBounded(ctx, f, budget)
			}
		}
	}
	return nil, fmt.Errorf("%w: %q", errEPUBEntryNotFound, name)
}

func readZipFileBounded(ctx context.Context, f *zip.File, budget *epubReadBudget) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	limit := int64(epubMaxEntryBytes)
	if budget != nil && budget.remaining < limit {
		limit = budget.remaining
	}
	if limit < 0 {
		limit = 0
	}
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > int64(epubMaxEntryBytes) {
		return nil, fmt.Errorf("%w: epub entry %q exceeds %d bytes limit", repository.ErrInvalidArgument, f.Name, epubMaxEntryBytes)
	}
	if budget != nil {
		if int64(len(data)) > budget.remaining {
			return nil, fmt.Errorf("%w: epub total uncompressed size exceeds %d bytes limit", repository.ErrInvalidArgument, epubMaxTotalBytes)
		}
		budget.remaining -= int64(len(data))
	}
	return data, nil
}

// resolveHref 把 OPF/NCX 内相对路径解析为 zip 内绝对路径。
func resolveHref(baseDir, href string) string {
	href = strings.SplitN(href, "#", 2)[0]
	if href == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(href); err == nil {
		href = decoded
	}
	if strings.HasPrefix(href, "/") {
		return path.Clean(href)
	}
	return path.Clean(path.Join(baseDir, href))
}

// checkEPUBDRM 仅拦截真实内容加密；字体混淆放行。
// epubEncryption 只解析判定 DRM 所需的最小结构。
type epubEncryption struct {
	EncryptedData []struct {
		EncryptionMethod struct {
			Algorithm string `xml:"Algorithm,attr"`
		} `xml:"EncryptionMethod"`
		CipherData struct {
			CipherReference struct {
				URI string `xml:"URI,attr"`
			} `xml:"CipherReference"`
		} `xml:"CipherData"`
	} `xml:"EncryptedData"`
}

// checkEPUBDRM 依据 META-INF/encryption.xml 判定内容是否受 DRM 保护：
// 正文/章节资源被加密即为 DRM；仅字体混淆（.ttf/.otf/.woff）属于合法用途，放行。
func checkEPUBDRM(ctx context.Context, zr *zip.Reader, budget *epubReadBudget) error {
	raw, err := readZipByName(ctx, zr, "META-INF/encryption.xml", budget)
	if err != nil {
		if errors.Is(err, errEPUBEntryNotFound) {
			return nil // 没有加密清单即未加密
		}
		return err
	}
	lower := strings.ToLower(string(raw))
	for _, algo := range []string{"adept", "lcp", "marvin"} {
		if strings.Contains(lower, algo) {
			return ErrDRMProtected
		}
	}
	var enc epubEncryption
	if err := xml.Unmarshal(raw, &enc); err != nil {
		return ErrDRMProtected
	}
	for _, ed := range enc.EncryptedData {
		uri := strings.ToLower(strings.TrimSpace(ed.CipherData.CipherReference.URI))
		switch {
		case strings.HasSuffix(uri, ".ttf"), strings.HasSuffix(uri, ".otf"),
			strings.HasSuffix(uri, ".woff"), strings.HasSuffix(uri, ".woff2"):
			continue // 字体混淆：允许
		default:
			return ErrDRMProtected
		}
	}
	return nil
}

// extractEPUBText 把 XHTML 正文按块级元素切分成纯文本。
func extractEPUBText(data []byte) (string, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	walkEPUBText(doc, &b)
	return normalizeText(b.String()), nil
}

func walkEPUBText(n *html.Node, b *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(n.Data)
		return
	case html.ElementNode:
		switch n.Data {
		case "script", "style", "head", "title":
			return
		case "br":
			b.WriteString("\n")
			return
		case "p", "div", "h1", "h2", "h3", "h4", "h5", "h6", "li", "tr",
			"section", "article", "blockquote", "pre":
			b.WriteString("\n\n")
			defer b.WriteString("\n\n")
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkEPUBText(c, b)
	}
}

// flattenNCX 先序打平 NCX 目录。
func flattenNCX(ctx context.Context, points []ncxPoint, out *[]tocEntry) error {
	for _, p := range points {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(*out) >= epubMaxTOCEntries {
			return fmt.Errorf("%w: epub toc entries exceed %d limit", repository.ErrInvalidArgument, epubMaxTOCEntries)
		}
		*out = append(*out, tocEntry{
			Title:    strings.TrimSpace(p.Label),
			Href:     p.Src,
			HasChild: len(p.Points) > 0,
		})
		if err := flattenNCX(ctx, p.Points, out); err != nil {
			return err
		}
	}
	return nil
}

// parseEPUBNav 解析 EPUB3 nav 文档中的目录列表。
func parseEPUBNav(ctx context.Context, data []byte) ([]tocEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, nil
	}
	var entries []tocEntry
	var findNav func(n *html.Node) *html.Node
	findNav = func(n *html.Node) *html.Node {
		if n.Type == html.ElementNode && n.Data == "nav" {
			for _, a := range n.Attr {
				if a.Key == "epub:type" && strings.Contains(a.Val, "toc") {
					return n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if found := findNav(c); found != nil {
				return found
			}
		}
		return nil
	}
	nav := findNav(doc)
	if nav == nil {
		return nil, nil
	}
	var walk func(n *html.Node) error
	walk = func(n *html.Node) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			title := strings.TrimSpace(textOfNode(n))
			href := ""
			for _, a := range n.Attr {
				if a.Key == "href" {
					href = a.Val
				}
			}
			if title != "" {
				if len(entries) >= epubMaxTOCEntries {
					return fmt.Errorf("%w: epub toc entries exceed %d limit", repository.ErrInvalidArgument, epubMaxTOCEntries)
				}
				entries = append(entries, tocEntry{Title: title, Href: href})
			}
			return nil
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if err := walk(c); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(nav); err != nil {
		return nil, err
	}
	return entries, nil
}

func textOfNode(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

// parseEPUB 解析 EPUB：元数据、封面、目录与正文。
func (s *ImportService) parseEPUB(ctx context.Context, file *os.File, size int64, fallbackTitle string) (*parsedBook, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	zr, err := zip.NewReader(file, size)
	if err != nil {
		return nil, nil, fmt.Errorf("epub: open zip: %w", err)
	}
	budget := &epubReadBudget{remaining: epubMaxTotalBytes}
	if err := checkEPUBDRM(ctx, zr, budget); err != nil {
		return nil, nil, err
	}

	containerRaw, err := readZipByName(ctx, zr, "META-INF/container.xml", budget)
	if err != nil {
		return nil, nil, fmt.Errorf("epub: %w", err)
	}
	var ctr epubContainer
	if err := xml.Unmarshal(containerRaw, &ctr); err != nil || len(ctr.Rootfiles) == 0 {
		return nil, nil, fmt.Errorf("epub: invalid container.xml")
	}
	opfPath := path.Clean(ctr.Rootfiles[0].FullPath)
	opfDir := path.Dir(opfPath)

	opfRaw, err := readZipByName(ctx, zr, opfPath, budget)
	if err != nil {
		return nil, nil, fmt.Errorf("epub: %w", err)
	}
	var pkg epubPackage
	if err := xml.Unmarshal(opfRaw, &pkg); err != nil {
		return nil, nil, fmt.Errorf("epub: invalid opf: %w", err)
	}

	pb := &parsedBook{}
	pb.Title = strings.TrimSpace(pkg.Metadata.Title)
	if pb.Title == "" {
		pb.Title = fallbackTitle
	}
	if len(pkg.Metadata.Creator) > 0 {
		pb.Author = strings.TrimSpace(pkg.Metadata.Creator[0])
	}

	manifest := make(map[string]epubManifestItem, len(pkg.Manifest.Items))
	for _, it := range pkg.Manifest.Items {
		manifest[it.ID] = it
	}

	// 封面：优先 meta[name=cover] 指向的条目，其次 properties 含 cover-image。
	coverID := ""
	for _, m := range pkg.Metadata.Meta {
		if strings.EqualFold(m.Name, "cover") {
			coverID = m.Content
		}
	}
	for _, it := range pkg.Manifest.Items {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if coverID != "" && it.ID == coverID {
			data, cerr := readZipByName(ctx, zr, resolveHref(opfDir, it.Href), budget)
			if cerr != nil && !errors.Is(cerr, errEPUBEntryNotFound) {
				return nil, nil, cerr
			}
			if cerr == nil {
				pb.Cover = data
				pb.CoverExt = strings.TrimPrefix(path.Ext(it.Href), ".")
			}
			break
		}
		if pb.Cover == nil && strings.Contains(it.Properties, "cover-image") {
			data, cerr := readZipByName(ctx, zr, resolveHref(opfDir, it.Href), budget)
			if cerr != nil && !errors.Is(cerr, errEPUBEntryNotFound) {
				return nil, nil, cerr
			}
			if cerr == nil {
				pb.Cover = data
				pb.CoverExt = strings.TrimPrefix(path.Ext(it.Href), ".")
			}
		}
	}

	// 目录：EPUB3 nav 优先，回退 EPUB2 NCX。
	var toc []tocEntry
	for _, it := range pkg.Manifest.Items {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if strings.Contains(it.Properties, "nav") {
			data, nerr := readZipByName(ctx, zr, resolveHref(opfDir, it.Href), budget)
			if nerr != nil && !errors.Is(nerr, errEPUBEntryNotFound) {
				return nil, nil, nerr
			}
			if nerr == nil {
				parsedTOC, terr := parseEPUBNav(ctx, data)
				if terr != nil {
					return nil, nil, terr
				}
				toc = parsedTOC
			}
			break
		}
	}
	if len(toc) == 0 {
		if ncxItem, ok := manifest["ncx"]; ok {
			data, nerr := readZipByName(ctx, zr, resolveHref(opfDir, ncxItem.Href), budget)
			if nerr != nil && !errors.Is(nerr, errEPUBEntryNotFound) {
				return nil, nil, nerr
			}
			if nerr == nil {
				var ncx epubNCX
				if xml.Unmarshal(data, &ncx) == nil {
					if ferr := flattenNCX(ctx, ncx.NavMap.Points, &toc); ferr != nil {
						return nil, nil, ferr
					}
				}
			}
		}
	}

	// 正文顺序：spine。
	spineOrder := make([]string, 0, len(pkg.Spine.ItemRefs))
	for _, ref := range pkg.Spine.ItemRefs {
		if it, ok := manifest[ref.IDRef]; ok {
			spineOrder = append(spineOrder, resolveHref(opfDir, it.Href))
		}
	}

	type cachedSpineText struct {
		text string
		ok   bool
	}
	spineCache := make(map[string]cachedSpineText, len(spineOrder))
	readSpineText := func(target string) (string, bool, error) {
		if cached, hit := spineCache[target]; hit {
			return cached.text, cached.ok, nil
		}
		data, rerr := readZipByName(ctx, zr, target, budget)
		if rerr != nil {
			if errors.Is(rerr, errEPUBEntryNotFound) {
				spineCache[target] = cachedSpineText{}
				return "", false, nil
			}
			return "", false, rerr
		}
		text, perr := extractEPUBText(data)
		if perr != nil {
			spineCache[target] = cachedSpineText{}
			return "", false, nil
		}
		spineCache[target] = cachedSpineText{text: text, ok: true}
		return text, true, nil
	}

	var chapters []parsedChapter
	used := make(map[string]bool)
	for _, entry := range toc {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		target := resolveHref(opfDir, entry.Href)
		text, ok := "", false
		if target != "" {
			var rerr error
			text, ok, rerr = readSpineText(target)
			if rerr != nil {
				return nil, nil, rerr
			}
		}
		if !ok {
			// 纯父级节点 → 卷名行；
			if entry.HasChild {
				chapters = append(chapters, parsedChapter{Title: entry.Title, IsVolume: true})
			}
			continue
		}
		used[target] = true
		chapters = append(chapters, parsedChapter{Title: entry.Title, Content: text})
	}

	// 目录未覆盖的 spine 文档追加为章节（以文档首个标题或序号命名）。
	for _, target := range spineOrder {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if used[target] {
			continue
		}
		text, ok, rerr := readSpineText(target)
		if rerr != nil {
			return nil, nil, rerr
		}
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if len(chapters) >= epubMaxTOCEntries {
			return nil, nil, fmt.Errorf("%w: epub chapter count exceeds %d limit", repository.ErrInvalidArgument, epubMaxTOCEntries)
		}
		chapters = append(chapters, parsedChapter{
			Title:   fmt.Sprintf("第 %d 节", len(chapters)+1),
			Content: text,
		})
	}

	if len(chapters) == 0 {
		return nil, nil, fmt.Errorf("%w: no readable chapters in epub", repository.ErrInvalidArgument)
	}
	pb.Chapters = chapters
	return pb, nil, nil
}
