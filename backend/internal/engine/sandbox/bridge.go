package sandbox

import (
	"context"
	"crypto/aes"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// installBridge 注册白名单桥接对象（java / source / book）与只读全局，
// 全部闭包捕获 *execCtx 以便访问当前执行周期的 ctx 与 HTTP 客户端。
// 白名单之外（file.* / process / exec / require / Packages / 反射）一律不注入。
func installBridge(rt *goja.Runtime, ec *execCtx) {
	java := rt.NewObject()
	java.Set("ajax", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		return ec.doHTTP(rt, http.MethodGet, url, nil)
	})
	java.Set("get", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		return ec.doHTTP(rt, http.MethodGet, url, nil)
	})
	java.Set("post", func(call goja.FunctionCall) goja.Value {
		url := call.Argument(0).String()
		data := call.Argument(1).String()
		return ec.doHTTP(rt, http.MethodPost, url, []byte(data))
	})
	// java.put(key, value) 写入书源级变量池（对齐 Legado JsExtensions）；
	// 返回写入值以支持 `result = java.put(...)` 这类链式写法。
	java.Set("put", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		val := call.Argument(1).Export()
		ec.sourceVars[name] = val
		return call.Argument(1)
	})
	java.Set("base64Encode", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(base64.StdEncoding.EncodeToString([]byte(call.Argument(0).String())))
	})
	java.Set("base64Decode", func(call goja.FunctionCall) goja.Value {
		b, err := base64.StdEncoding.DecodeString(call.Argument(0).String())
		if err != nil {
			return rt.ToValue("")
		}
		return rt.ToValue(string(b))
	})
	java.Set("md5Encode", func(call goja.FunctionCall) goja.Value {
		sum := md5.Sum([]byte(call.Argument(0).String()))
		return rt.ToValue(hex.EncodeToString(sum[:]))
	})
	java.Set("aesEncode", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(aesEncrypt(call.Argument(0).String(), call.Argument(1).String()))
	})
	java.Set("aesDecode", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(aesDecrypt(call.Argument(0).String(), call.Argument(1).String()))
	})
	java.Set("timeFormat", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(formatTime(time.Now(), call.Argument(0).String()))
	})
	java.Set("log", func(call goja.FunctionCall) goja.Value {
		return goja.Undefined()
	})
	rt.Set("java", java)

	source := rt.NewObject()
	source.Set("getVariable", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		if v, ok := ec.sourceVars[name]; ok {
			return rt.ToValue(v)
		}
		return goja.Null()
	})
	source.Set("setVariable", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		val := call.Argument(1).Export()
		ec.sourceVars[name] = val
		return goja.Undefined()
	})
	rt.Set("source", source)

	// book 只读字段（name/author/bookUrl），由 setVars 在每个执行周期注入值。
	book := rt.NewObject()
	book.Set("name", goja.Undefined())
	book.Set("author", goja.Undefined())
	book.Set("bookUrl", goja.Undefined())
	rt.Set("book", book)
}

// doHTTP 通过 HTTPClient 出网，ctx 透传保证超时瞬间取消阻塞 I/O。
func (ec *execCtx) doHTTP(rt *goja.Runtime, method, url string, body []byte) goja.Value {
	if ec.client == nil {
		ec.lastErr = fmt.Errorf("%w: no HTTP client configured", ErrRuleExecution)
		panic(rt.NewGoError(ec.lastErr))
	}
	resp, err := ec.client.Do(ec.ctx, method, url, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			ec.lastErr = ErrSandboxTimeout
		} else {
			ec.lastErr = fmt.Errorf("%w: %v", ErrRuleExecution, err)
		}
		panic(rt.NewGoError(err))
	}
	return rt.ToValue(string(resp))
}

// formatTime 支持 yyyy/MM/dd/HH/mm/ss 的最小 SimpleDateFormat 子集。
func formatTime(t time.Time, pattern string) string {
	return strings.NewReplacer(
		"yyyy", t.Format("2006"),
		"MM", t.Format("01"),
		"dd", t.Format("02"),
		"HH", t.Format("15"),
		"mm", t.Format("04"),
		"ss", t.Format("05"),
	).Replace(pattern)
}

// aesEncrypt / aesDecrypt：AES-128-ECB + PKCS7，key 取 MD5(keyStr) 前 16 字节。
// 占位实现，后续 Stage 对齐 Legado 的 CBC 语义。
func aesKey(key string) []byte {
	sum := md5.Sum([]byte(key))
	return sum[:]
}

func aesEncrypt(plain, key string) string {
	if plain == "" {
		return ""
	}
	block, err := aes.NewCipher(aesKey(key))
	if err != nil {
		return ""
	}
	pad := pkcs7Pad([]byte(plain), aes.BlockSize)
	out := make([]byte, len(pad))
	for i := 0; i < len(pad); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], pad[i:i+aes.BlockSize])
	}
	return base64.StdEncoding.EncodeToString(out)
}

func aesDecrypt(cipher, key string) string {
	if cipher == "" {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(cipher)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return ""
	}
	block, err := aes.NewCipher(aesKey(key))
	if err != nil {
		return ""
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
	}
	unpadded, err := pkcs7Unpad(out)
	if err != nil {
		return ""
	}
	return string(unpadded)
}

func pkcs7Pad(b []byte, blockSize int) []byte {
	n := blockSize - len(b)%blockSize
	for i := 0; i < n; i++ {
		b = append(b, byte(n))
	}
	return b
}

func pkcs7Unpad(b []byte) ([]byte, error) {
	if len(b) == 0 {
		return nil, errors.New("empty")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > len(b) || n > aes.BlockSize {
		return nil, errors.New("bad padding")
	}
	return b[:len(b)-n], nil
}
