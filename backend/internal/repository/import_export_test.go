//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"yreader/backend/internal/repository/testutil"
)

// deepEqualJSON 语义比较两段 JSON（jsonb 会重排键、归一化空白，故不能比对字节）。
func deepEqualJSON(t *testing.T, a, b []byte) bool {
	t.Helper()
	var va, vb any
	if err := json.Unmarshal(a, &va); err != nil {
		t.Fatalf("解析 a 失败: %v\n%s", err, a)
	}
	if err := json.Unmarshal(b, &vb); err != nil {
		t.Fatalf("解析 b 失败: %v\n%s", err, b)
	}
	return reflect.DeepEqual(va, vb)
}

func rawURL(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("解析书源失败: %v", err)
	}
	var u string
	_ = json.Unmarshal(obj["bookSourceUrl"], &u)
	return u
}

// canonicalSource 把原始书源 JSON 归一为导出应得的形状：13 个 camelCase 关系列
// 按 DB 语义覆写（缺省值/钳制规则与 repository.parseSource 一致），未知键保留。
func canonicalSource(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("canonical unmarshal: %v", err)
	}
	url, _ := m["bookSourceUrl"].(string)
	name, _ := m["bookSourceName"].(string)
	if name == "" {
		name = url
	}
	group, _ := m["bookSourceGroup"].(string)
	if group == "" {
		group = "默认"
	}
	bsType := jsonNum(m["bookSourceType"])
	if bsType < 0 || bsType > 2 {
		bsType = 0
	}
	enabled := true
	if b, ok := m["enabled"].(bool); ok {
		enabled = b
	}
	enabledExplore := true
	if b, ok := m["enabledExplore"].(bool); ok {
		enabledExplore = b
	}
	weight := jsonNum(m["weight"])
	respond := jsonNum(m["respondTime"])
	if respond < 0 {
		respond = 0
	}
	m["bookSourceUrl"] = url
	m["bookSourceName"] = name
	m["bookSourceGroup"] = group
	m["bookSourceType"] = bsType
	m["enabled"] = enabled
	m["enabledExplore"] = enabledExplore
	m["weight"] = weight
	m["respondTime"] = respond
	m["charset"] = asString(m["charset"])
	m["concurrentRate"] = asString(m["concurrentRate"])
	m["loginUrl"] = asString(m["loginUrl"])
	m["loginCheckJs"] = asString(m["loginCheckJs"])
	m["header"] = canonicalHeader(m["header"])
	return m
}

func jsonNum(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func canonicalHeader(v any) any {
	switch h := v.(type) {
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(h), &m) == nil {
			return m
		}
		return map[string]any{}
	case map[string]any:
		return h
	}
	return map[string]any{}
}

// sourceExportEqual 断言「原始 JSON 归一后」与「导出 JSON」语义一致。
func sourceExportEqual(t *testing.T, orig, exported json.RawMessage) bool {
	t.Helper()
	want := canonicalSource(t, orig)
	var got map[string]any
	if err := json.Unmarshal(exported, &got); err != nil {
		t.Fatalf("解析导出失败: %v", err)
	}
	return reflect.DeepEqual(want, got)
}

func handcraftedSources() []string {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	return []string{
		// 1. 顶层未知键
		`{"bookSourceUrl":"https://h.example/1","bookSourceName":"手工一","customTop":{"a":1,"b":[true,false]}}`,
		// 2. search 组内未知键
		`{"bookSourceUrl":"https://h.example/2","bookSourceName":"手工二","ruleSearch":{"bookList":"//div[@class='x']","unknownInSearch":{"k":"v"}}}`,
		// 3. toc 嵌套对象 + Unicode
		`{"bookSourceUrl":"https://h.example/3","bookSourceName":"手工三·中文","ruleToc":{"chapterList":"//a","nested":{"deep":["一","二","三"]}}}`,
		// 4. null 值
		`{"bookSourceUrl":"https://h.example/4","bookSourceName":"手工四","ruleContent":null,"charset":null}`,
		// 5. 空数组
		`{"bookSourceUrl":"https://h.example/5","bookSourceName":"手工五","emptyArr":[]}`,
		// 6. 数字/布尔未知键
		`{"bookSourceUrl":"https://h.example/6","bookSourceName":"手工六","unknownNum":3.14,"unknownBool":false}`,
		// 7. >4KB 超长字符串
		fmt.Sprintf(`{"bookSourceUrl":"https://h.example/7","bookSourceName":"手工七","longField":"%s"}`, string(long)),
		// 8. JS 规则 + 替换净化
		`{"bookSourceUrl":"https://h.example/8","bookSourceName":"手工八","ruleContent":{"content":"@js:java.ajax(result)","replaceRegex":"##<p>.*?</p>##"}}`,
		// 9. header 为字符串套 JSON
		`{"bookSourceUrl":"https://h.example/9","bookSourceName":"手工九","header":"{\"User-Agent\":\"legado\"}"}`,
		// 10. 顶层未知键 + enabled/weight
		`{"bookSourceUrl":"https://h.example/10","bookSourceName":"手工十","enabled":false,"weight":99,"extraTop":"保留"}`,
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewImportExportRepository()
	ctx := context.Background()

	sources := handcraftedSources()
	raws := make([]json.RawMessage, len(sources))
	for i, s := range sources {
		raws[i] = json.RawMessage(s)
	}

	rep, err := repo.Import(ctx, tx, userID, raws, ImportOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(rep.IDs) != len(sources) || rep.Success != len(sources) {
		t.Fatalf("Import 返回 %d id / success %d, want %d", len(rep.IDs), rep.Success, len(sources))
	}

	exported, err := repo.Export(ctx, tx, userID, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(exported) != len(sources) {
		t.Fatalf("Export 返回 %d 条, want %d", len(exported), len(sources))
	}

	origByURL := map[string]json.RawMessage{}
	for _, r := range raws {
		origByURL[rawURL(t, r)] = r
	}
	for _, e := range exported {
		u := rawURL(t, e)
		orig, ok := origByURL[u]
		if !ok {
			t.Fatalf("导出出现未知 URL: %s", u)
		}
		if !sourceExportEqual(t, orig, e) {
			t.Fatalf("round-trip 值不一致: %s\norig=%s\nexported=%s", u, orig, e)
		}
	}
}

func TestImportExport1000(t *testing.T) {
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	userID := mustSeedUser(t, tx)
	repo := NewImportExportRepository()
	ctx := context.Background()

	const n = 1000
	raws := make([]json.RawMessage, n)
	for i := 0; i < n; i++ {
		raws[i] = json.RawMessage(fmt.Sprintf(
			`{"bookSourceUrl":"https://gen.example/%d","bookSourceName":"生成源%03d","extraTop":{"idx":%d,"tags":["a","b"]},"ruleSearch":{"bookList":"//div[%d]","unknown":"v%d"}}`,
			i, i, i, i, i))
	}

	rep, err := repo.Import(ctx, tx, userID, raws, ImportOptions{})
	if err != nil {
		t.Fatalf("Import 1000: %v", err)
	}
	if len(rep.IDs) != n {
		t.Fatalf("Import 返回 %d, want %d", len(rep.IDs), n)
	}

	exported, err := repo.Export(ctx, tx, userID, nil)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(exported) != n {
		t.Fatalf("Export 返回 %d, want %d", len(exported), n)
	}

	origByURL := map[string]json.RawMessage{}
	for _, r := range raws {
		origByURL[rawURL(t, r)] = r
	}
	for _, e := range exported {
		u := rawURL(t, e)
		orig, ok := origByURL[u]
		if !ok {
			t.Fatalf("导出出现未知 URL: %s", u)
		}
		if !sourceExportEqual(t, orig, e) {
			t.Fatalf("1000 条 round-trip 值不一致: %s\norig=%s\nexported=%s", u, orig, e)
		}
	}
}

func TestLegadoSamples(t *testing.T) {
	dir := filepath.Join("testdata", "legado_samples")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Skip("testdata/legado_samples 为空或不存在，跳过外部样本测试")
	}
	pool := testutil.TestPool(t)
	tx := testutil.BeginTx(t, pool)
	repo := NewImportExportRepository()
	ctx := context.Background()

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读取 %s: %v", e.Name(), err)
		}
		var arr []json.RawMessage
		if err := json.Unmarshal(data, &arr); err != nil {
			// 单个对象也允许
			var one json.RawMessage
			if err2 := json.Unmarshal(data, &one); err2 != nil {
				t.Fatalf("%s 既不是数组也不是对象: %v", e.Name(), err)
			}
			arr = []json.RawMessage{one}
		}
		// 存储契约要求 http(s) URL；真实样本含少量自定义 bookSourceUrl
		// （如 "DQuestQBall"），无法入库，按契约过滤并记录。同一文件内
		// 也可能出现重复 URL，按 (user_id, book_source_url) 唯一键去重。
		valid := make([]json.RawMessage, 0, len(arr))
		seen := map[string]bool{}
		skipped, dup := 0, 0
		for _, r := range arr {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(r, &obj); err != nil {
				skipped++
				continue
			}
			var u string
			_ = json.Unmarshal(obj["bookSourceUrl"], &u)
			if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
				skipped++
				continue
			}
			if seen[u] {
				dup++
				continue
			}
			seen[u] = true
			valid = append(valid, r)
		}
		t.Logf("%s: 合法 %d 条，跳过非 http(s) %d 条，同文件重复 URL %d 条", e.Name(), len(valid), skipped, dup)
		if len(valid) == 0 {
			continue
		}
		// 每个文件用独立用户导入，避免跨文件重复 URL 撞唯一键。
		fileUser := mustSeedUser(t, tx)
		if _, err := repo.Import(ctx, tx, fileUser, valid, ImportOptions{}); err != nil {
			t.Fatalf("导入样本 %s 失败: %v", e.Name(), err)
		}
		exported, err := repo.Export(ctx, tx, fileUser, nil)
		if err != nil {
			t.Fatalf("导出样本 %s 失败: %v", e.Name(), err)
		}
		if len(exported) != len(valid) {
			t.Fatalf("%s 导入 %d 条、导出 %d 条，不一致", e.Name(), len(valid), len(exported))
		}
	}
}
