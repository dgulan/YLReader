package textnorm

import "testing"

func TestNormalizeBook(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Hello World", "helloworld"},
		{"斗破苍穹", "斗破苍穹"},
		{"斗 破 苍 穹", "斗破苍穹"},
		{"斗破-苍穹", "斗破苍穹"},
		{"ＤＯＵＰＯ", "doupo"},     // 全角字母 NFKC 折叠
		{"斗破\u200b苍穹", "斗破苍穹"}, // 零宽空格
		{" 斗破苍穹 ", "斗破苍穹"},
		{"《斗破苍穹》", "斗破苍穹"},
		{"ＡＢＣ１２３", "abc123"}, // 全角字母+数字
	}
	for _, c := range cases {
		if got := NormalizeBook(c.in); got != c.want {
			t.Errorf("NormalizeBook(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeAuthor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"作者：天蚕土豆", "天蚕土豆"},
		{"作者:天蚕土豆", "天蚕土豆"},
		{"天蚕土豆 著", "天蚕土豆"},
		{"天蚕土豆(著)", "天蚕土豆"},
		{"天蚕土豆（著）", "天蚕土豆"},
		{"著 天蚕土豆", "天蚕土豆"},
		{"作者：天蚕土豆 著", "天蚕土豆"},
		{"作者天蚕土豆", "天蚕土豆"}, // 无冒号前缀
	}
	for _, c := range cases {
		if got := NormalizeAuthor(c.in); got != c.want {
			t.Errorf("NormalizeAuthor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeChapter(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// 阿拉伯数字
		{"第120章 风起", "n:120:风起"},
		{"正文 12 番外", "n:12:番外"},
		{"第1章", "n:1"},
		{"第007章 风起", "n:7:风起"}, // 前导零折叠
		{"12", "n:12"},
		{"第3章 第2卷", "n:3:第2卷"}, // 多数字取首个
		// 中文数字
		{"第一百零八回", "n:108"},
		{"第一百零八回 风起", "n:108:风起"},
		{"第十二章 风起", "n:12:风起"},
		{"第一百二十章 风起", "n:120:风起"},
		{"第九章 归来", "n:9:归来"},
		{"第十回 归来", "n:10:归来"},
		{"第两百章", "n:200"}, // 两=2
		{"第三千章", "n:3000"},
		{"第零章", "n:0"},
		// 全角数字（NFKC 折叠）
		{"第１２章 风起", "n:12:风起"},
		// 罗马数字不解析为数字，回退纯标题
		{"第III章 风起", "第iii章风起"},
		{"第X章 风起", "第x章风起"},
		// 无章节号 → 纯归一化文本
		{"序章", "序章"},
		{"楔子", "楔子"},
		{"番外", "番外"},
		{"风起", "风起"},
		{"", ""},
		// 中文数字无边界误匹配修复（反例：普通词语开头的汉字数字不提取）
		{"《一念永恒》番外", "一念永恒番外"},
		{"九州风云录", "九州风云录"},
		{"十字路口", "十字路口"},
		{"百年孤独", "百年孤独"},
		// 合法章节序号上下文（无「第」前缀时靠量词/分隔符/纯数字判定）
		{"十二章", "n:12"},
		{"一百零八 风起", "n:108:风起"},
		{"一、初入", "n:1:初入"},
		{"十二", "n:12"},
		{"一百零八", "n:108"},
	}
	for _, c := range cases {
		if got := NormalizeChapter(c.in); got != c.want {
			t.Errorf("NormalizeChapter(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeChapterTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"第一卷 风起", "第一卷风起"},
		{"第 1 卷", "第1卷"},
		{"第一百零八回", "第一百零八回"}, // 卷名行不提取章节号
		{"卷首语", "卷首语"},
	}
	for _, c := range cases {
		if got := NormalizeChapterTitle(c.in); got != c.want {
			t.Errorf("NormalizeChapterTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestParseChineseNumeral 直接覆盖中文数字解析边界。
func TestParseChineseNumeral(t *testing.T) {
	cases := []struct {
		in   string
		want int
		ok   bool
	}{
		{"一", 1, true},
		{"十", 10, true},
		{"十二", 12, true},
		{"二十", 20, true},
		{"一百零八", 108, true},
		{"一百二十", 120, true},
		{"三百", 300, true},
		{"一千二百三十四", 1234, true},
		{"九千九百九十九", 9999, true},
		{"零", 0, true},
		{"两百", 200, true},
		{"一万", 0, false}, // 超出上限
	}
	for _, c := range cases {
		got, ok := parseChineseNumeral(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("parseChineseNumeral(%q) = (%d,%v), want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
