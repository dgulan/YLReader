package fetcher

import "errors"

// ErrUnsafeURL 表示目标 URL 在入参层即被拒绝：非法 scheme、含 userinfo
// 或端口非法。它与 ErrSSRFBlocked 的区别：前者是 URL 语法/形态不安全，
// 后者是解析出的 IP 命中内网/保留网段。
var ErrUnsafeURL = errors.New("fetcher: unsafe url")

// ErrUpstreamError 表示目标站抓取失败：5xx 重试耗尽，或网络/TLS 故障
// 重试耗尽。上层应通过 errors.Is 命中并映射为 502 source_request_failed。
var ErrUpstreamError = errors.New("fetcher: upstream error")
