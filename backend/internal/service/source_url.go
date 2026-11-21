package service

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/repository"
)

// ValidateSourceURL 是书源/抓取目标共用的入参层预检：
//   - 仅允许 http(s)，拒绝 userinfo 与非法端口；
//   - IP 字面量命中环回/私网/链路本地等直接拒绝；
//   - 域名留给 fetcher.DialContext 在连接阶段按解析后的真实 IP 兜底，
//     从而避免解析与拨号之间的 DNS 重绑 TOCTOU。
func ValidateSourceURL(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Hostname() == "" {
		return fmt.Errorf("%w: invalid book_source_url", repository.ErrInvalidArgument)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: book_source_url must be http(s)", repository.ErrInvalidArgument)
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return repository.ErrInvalidArgument
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return repository.ErrInvalidArgument
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && fetcher.IsBlockedIP(ip) {
		return fetcher.ErrSSRFBlocked
	}
	return nil
}
