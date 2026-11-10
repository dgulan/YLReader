package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/engine/fetcher"
	"yreader/backend/internal/engine/parser"
	"yreader/backend/internal/engine/sandbox"
	"yreader/backend/internal/repository"
)

// DebugService 执行单条书源规则的逐级追踪（/sources/{id}/debug）。
type DebugService struct {
	pool    *pgxpool.Pool
	sources *repository.SourceRepository
	ft      Fetcher
	sb      *sandbox.Sandbox
}

// NewDebugService 构造规则调试服务。
func NewDebugService(pool *pgxpool.Pool, ft Fetcher, sb *sandbox.Sandbox) *DebugService {
	return &DebugService{
		pool:    pool,
		sources: repository.NewSourceRepository(),
		ft:      ft,
		sb:      sb,
	}
}

// Debug 执行规则调试。
//
// 返回值 err 仅表示「进入解析引擎之前」的失败（书源不存在、输入无法获取、
// 抓取失败）；此时 steps 为空，HTTP 层返回顶层错误。规则执行阶段失败会体现
// 在 steps 的最后一个失败步骤（error_code 已填充），err 为 nil。
func (s *DebugService) Debug(
	ctx context.Context,
	userID, sourceID uuid.UUID,
	ruleType, rule string,
	rawURL *string,
	variables map[string]any,
	maxPreviewBytes int,
) (string, []parser.StepTrace, error) {
	src, err := s.sources.Get(ctx, s.pool, userID, sourceID)
	if err != nil {
		return "", nil, err
	}

	input, err := s.resolveInput(ctx, src, rawURL, variables)
	if err != nil {
		return "", nil, err
	}

	engine := parser.New(s.sb)
	engine.SetVariable("baseUrl", src.BookSourceURL)
	for k, v := range variables {
		engine.SetVariableAny(k, v)
	}
	engine.SetVariableAny("result", input)

	result, steps, evalErr := engine.EvalWithTrace(ctx, ruleType, rule, input, maxPreviewBytes)
	if evalErr != nil && len(steps) == 0 {
		return "", nil, evalErr
	}
	return result, steps, nil
}

// resolveInput 按 html > content > 请求 url > 书源 url 的优先级确定输入文本。
func (s *DebugService) resolveInput(ctx context.Context, src *repository.BookSource, rawURL *string, variables map[string]any) (string, error) {
	for _, key := range []string{"html", "content"} {
		if v, ok := variables[key]; ok {
			if str, ok := v.(string); ok && str != "" {
				return str, nil
			}
		}
	}
	target := src.BookSourceURL
	if rawURL != nil && strings.TrimSpace(*rawURL) != "" {
		target = strings.TrimSpace(*rawURL)
	}
	resp, err := s.ft.Get(ctx, target, nil)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		// 4xx/5xx 直接按抓取失败处理：绝不把错误页 HTML 送入规则引擎。
		return "", fmt.Errorf("%w: upstream status %d", fetcher.ErrUpstreamError, resp.StatusCode)
	}
	return string(resp.Body), nil
}
