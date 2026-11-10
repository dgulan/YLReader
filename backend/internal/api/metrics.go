package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

// S5-4：architecture §8.2 声明的可观测指标。
// 全部使用带标签的 CounterVec；CounterVec 在首次 WithLabelValues(...) 打点前
// 不会导出任何 series，因此必须在包初始化时预置零值（prewarmZeroSeries），
// 否则 /metrics 的输出里看不到这些指标名。
var (
	// YreaderJobsTotal 任务累计数（按任务类型与终态/进行态）。
	YreaderJobsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yreader_jobs_total",
			Help: "Total number of background jobs by type and status.",
		},
		[]string{"type", "status"},
	)

	// YreaderSandboxExecutionsTotal 规则沙箱执行累计数（按结果）。
	YreaderSandboxExecutionsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yreader_sandbox_executions_total",
			Help: "Total number of sandbox script executions by outcome.",
		},
		[]string{"outcome"},
	)

	// YreaderSSRFBlockedTotal SSRF 拦截累计数（按拦截原因）。
	YreaderSSRFBlockedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "yreader_ssrf_blocked_total",
			Help: "Total number of blocked outbound requests by reason.",
		},
		[]string{"reason"},
	)
)

// jobTypes / jobStatuses / sandboxOutcomes / ssrfReasons 与 architecture §8.2 的枚举对齐。
var (
	jobTypes        = []string{"toc_refresh", "chapter_prefetch", "full_cache", "source_health", "import_sources"}
	jobStatuses     = []string{"queued", "running", "succeeded", "failed", "canceled"}
	sandboxOutcomes = []string{"ok", "timeout", "error", "panic"}
	ssrfReasons     = []string{"private", "loopback", "mapped", "nat64", "other"}
)

// prewarmZeroSeries 让未打点的 CounterVec 在 /metrics 中以 0 值 series 出现。
func prewarmZeroSeries() {
	for _, t := range jobTypes {
		for _, s := range jobStatuses {
			YreaderJobsTotal.WithLabelValues(t, s).Add(0)
		}
	}
	for _, o := range sandboxOutcomes {
		YreaderSandboxExecutionsTotal.WithLabelValues(o).Add(0)
	}
	for _, r := range ssrfReasons {
		YreaderSSRFBlockedTotal.WithLabelValues(r).Add(0)
	}
}

func init() {
	prometheus.MustRegister(YreaderJobsTotal, YreaderSandboxExecutionsTotal, YreaderSSRFBlockedTotal)
	prewarmZeroSeries()
}
