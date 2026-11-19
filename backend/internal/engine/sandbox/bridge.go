package sandbox

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/dop251/goja"
	"github.com/google/uuid"
)

// installBridge 注册白名单桥接对象（java / source / book）与只读全局，
// 全部闭包捕获 *execCtx 以便访问当前执行周期的 ctx 与 HTTP 客户端。
// 白名单之外（file.* / process / exec / require / Packages / 反射）一律不注入。
func installBridge(rt *goja.Runtime, ec *execCtx) {
	java := rt.NewObject()
	java.Set("randomUUID", func() string { return uuid.NewString() })
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
		return rt.ToValue(evalAESEncode(call))
	})
	java.Set("aesDecode", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(evalAESDecode(call))
	})
	java.Set("aesBase64DecodeToString", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(evalAESDecode(call))
	})
	java.Set("aesDecodeToString", func(call goja.FunctionCall) goja.Value {
		return rt.ToValue(evalAESDecode(call))
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

const maxAESInputBytes = 4 << 20

func evalAESEncode(call goja.FunctionCall) string {
	data := call.Argument(0).String()
	key := call.Argument(1).String()
	if len(call.Arguments) <= 2 {
		return aesEncrypt(data, key)
	}
	transform := call.Argument(2).String()
	iv := ""
	if len(call.Arguments) >= 4 && !goja.IsUndefined(call.Argument(3)) && !goja.IsNull(call.Argument(3)) {
		iv = call.Argument(3).String()
	}
	if strings.TrimSpace(transform) == "" {
		return aesEncrypt(data, key)
	}
	return aesEncryptWithMode(data, key, transform, iv)
}

func evalAESDecode(call goja.FunctionCall) string {
	data := call.Argument(0).String()
	key := call.Argument(1).String()
	if len(call.Arguments) <= 2 {
		return aesDecrypt(data, key)
	}
	transform := call.Argument(2).String()
	iv := ""
	if len(call.Arguments) >= 4 && !goja.IsUndefined(call.Argument(3)) && !goja.IsNull(call.Argument(3)) {
		iv = call.Argument(3).String()
	}
	if strings.TrimSpace(transform) == "" {
		return aesDecrypt(data, key)
	}
	return aesDecryptWithMode(data, key, transform, iv)
}

// aesEncrypt / aesDecrypt：两参数默认实现（AES-128-ECB + PKCS7，key 取 MD5(keyStr)）。
func aesKey(key string) []byte {
	sum := md5.Sum([]byte(key))
	return sum[:]
}

func aesRawOrMD5Key(key string) []byte {
	kb := []byte(key)
	switch len(kb) {
	case 16, 24, 32:
		return kb
	default:
		sum := md5.Sum(kb)
		return sum[:]
	}
}

func aesIVBytes(iv string) []byte {
	buf := make([]byte, aes.BlockSize)
	copy(buf, []byte(iv))
	return buf
}

func parseAESTransformation(transform string) (mode, padding string, ok bool) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(transform)), "/")
	if len(parts) == 1 && parts[0] == "AES" {
		return "ECB", "PKCS7PADDING", true
	}
	if len(parts) != 3 || parts[0] != "AES" {
		return "", "", false
	}
	mode, padding = parts[1], parts[2]
	if mode != "ECB" && mode != "CBC" {
		return "", "", false
	}
	switch padding {
	case "PKCS5PADDING", "PKCS7PADDING", "NOPADDING":
		return mode, padding, true
	default:
		return "", "", false
	}
}

func aesEncryptWithMode(plain, key, transform, iv string) string {
	if plain == "" || len(plain) > maxAESInputBytes {
		return ""
	}
	mode, padding, ok := parseAESTransformation(transform)
	if !ok {
		return ""
	}
	block, err := aes.NewCipher(aesRawOrMD5Key(key))
	if err != nil {
		return ""
	}
	src := []byte(plain)
	if padding == "NOPADDING" {
		if len(src)%aes.BlockSize != 0 {
			return ""
		}
	} else {
		src = pkcs7Pad(src, aes.BlockSize)
	}
	out := make([]byte, len(src))
	if mode == "CBC" {
		cipher.NewCBCEncrypter(block, aesIVBytes(iv)).CryptBlocks(out, src)
	} else {
		for i := 0; i < len(src); i += aes.BlockSize {
			block.Encrypt(out[i:i+aes.BlockSize], src[i:i+aes.BlockSize])
		}
	}
	return base64.StdEncoding.EncodeToString(out)
}

func aesDecryptWithMode(cipherText, key, transform, iv string) string {
	if cipherText == "" || len(cipherText) > maxAESInputBytes {
		return ""
	}
	mode, padding, ok := parseAESTransformation(transform)
	if !ok {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(cipherText)
	if err != nil || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return ""
	}
	block, err := aes.NewCipher(aesRawOrMD5Key(key))
	if err != nil {
		return ""
	}
	out := make([]byte, len(data))
	if mode == "CBC" {
		cipher.NewCBCDecrypter(block, aesIVBytes(iv)).CryptBlocks(out, data)
	} else {
		for i := 0; i < len(data); i += aes.BlockSize {
			block.Decrypt(out[i:i+aes.BlockSize], data[i:i+aes.BlockSize])
		}
	}
	if padding == "NOPADDING" {
		return string(out)
	}
	unpadded, err := pkcs7Unpad(out)
	if err != nil {
		return ""
	}
	return string(unpadded)
}

func aesEncrypt(plain, key string) string {
	if plain == "" || len(plain) > maxAESInputBytes {
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
	if cipher == "" || len(cipher) > maxAESInputBytes {
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
	for _, value := range b[len(b)-n:] {
		if int(value) != n {
			return nil, errors.New("bad padding")
		}
	}
	return b[:len(b)-n], nil
}
