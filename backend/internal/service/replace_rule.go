package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"yreader/backend/internal/repository"
)

// ErrInvalidRegex 表示替换规则的 pattern 不是合法正则。
var ErrInvalidRegex = errors.New("invalid regex pattern")

// ReplaceRuleInput 是新建/更新替换规则的业务入参。
type ReplaceRuleInput struct {
	Name        string
	GroupName   string
	Pattern     string
	Replacement string
	Scope       string
	IsEnabled   bool
	OrderIndex  int32
}

// ReplaceRuleService 负责替换规则的 CRUD。
type ReplaceRuleService struct {
	pool *pgxpool.Pool
	repo *repository.ReplaceRuleRepository
	log  *slog.Logger
}

// NewReplaceRuleService 构造 ReplaceRuleService。
func NewReplaceRuleService(pool *pgxpool.Pool, log *slog.Logger) *ReplaceRuleService {
	if log == nil {
		log = slog.Default()
	}
	return &ReplaceRuleService{pool: pool, repo: repository.NewReplaceRuleRepository(), log: log}
}

func validateReplaceRuleInput(in *ReplaceRuleInput) error {
	if in.Name == "" {
		return fmt.Errorf("%w: name is required", repository.ErrInvalidArgument)
	}
	if in.Pattern == "" {
		return fmt.Errorf("%w: pattern is required", repository.ErrInvalidArgument)
	}
	if _, err := regexp.Compile(in.Pattern); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRegex, err)
	}
	switch in.Scope {
	case "", "content", "title", "all":
	default:
		return fmt.Errorf("%w: invalid scope %q", repository.ErrInvalidArgument, in.Scope)
	}
	return nil
}

// List 返回规则列表；enabled 为 nil 表示不过滤。
func (s *ReplaceRuleService) List(ctx context.Context, userID uuid.UUID, enabled *bool) ([]*repository.ReplaceRule, error) {
	items, err := s.repo.List(ctx, s.pool, userID, enabled)
	if err != nil {
		return nil, fmt.Errorf("list replace rules: %w", err)
	}
	if items == nil {
		items = []*repository.ReplaceRule{}
	}
	return items, nil
}

// Create 新建规则。
func (s *ReplaceRuleService) Create(ctx context.Context, userID uuid.UUID, in ReplaceRuleInput) (*repository.ReplaceRule, error) {
	if err := validateReplaceRuleInput(&in); err != nil {
		return nil, err
	}
	rule := &repository.ReplaceRule{
		UserID:      userID,
		Name:        in.Name,
		GroupName:   in.GroupName,
		Pattern:     in.Pattern,
		Replacement: in.Replacement,
		Scope:       in.Scope,
		IsEnabled:   in.IsEnabled,
		OrderIndex:  in.OrderIndex,
	}
	if rule.GroupName == "" {
		rule.GroupName = "默认"
	}
	if rule.Scope == "" {
		rule.Scope = "content"
	}
	if err := s.repo.Create(ctx, s.pool, rule); err != nil {
		return nil, fmt.Errorf("create replace rule: %w", err)
	}
	return rule, nil
}

// Update 更新规则；不存在返回 ErrNotFound。
func (s *ReplaceRuleService) Update(ctx context.Context, userID, ruleID uuid.UUID, in ReplaceRuleInput) (*repository.ReplaceRule, error) {
	if err := validateReplaceRuleInput(&in); err != nil {
		return nil, err
	}
	rule, err := s.repo.Get(ctx, s.pool, userID, ruleID)
	if err != nil {
		return nil, err
	}
	rule.Name = in.Name
	rule.Pattern = in.Pattern
	if in.GroupName != "" {
		rule.GroupName = in.GroupName
	}
	rule.Replacement = in.Replacement
	if in.Scope != "" {
		rule.Scope = in.Scope
	}
	rule.IsEnabled = in.IsEnabled
	rule.OrderIndex = in.OrderIndex
	if err := s.repo.Update(ctx, s.pool, rule); err != nil {
		return nil, err
	}
	return rule, nil
}

// Delete 删除规则；不存在返回 ErrNotFound。
func (s *ReplaceRuleService) Delete(ctx context.Context, userID, ruleID uuid.UUID) error {
	return s.repo.Delete(ctx, s.pool, userID, ruleID)
}
