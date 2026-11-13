package repository

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// BookType 对应 PostgreSQL 枚举 book_type。
type BookType string

const (
	BookTypeOnline BookType = "online"
	BookTypeLocal  BookType = "local"
)

// BookStatus 对应 PostgreSQL 枚举 book_status。
type BookStatus string

const (
	BookStatusUnknown     BookStatus = "unknown"
	BookStatusSerializing BookStatus = "serializing"
	BookStatusCompleted   BookStatus = "completed"
	BookStatusPaused      BookStatus = "paused"
)

// MappingStatus 对应 PostgreSQL 枚举 mapping_status。
type MappingStatus string

const (
	MappingStatusPending MappingStatus = "pending"
	MappingStatusActive  MappingStatus = "active"
	MappingStatusStale   MappingStatus = "stale"
	MappingStatusBroken  MappingStatus = "broken"
)

// OverrideStatus 对应 chapter_source_overrides.status 文本枚举。
type OverrideStatus string

const (
	OverrideStatusActive OverrideStatus = "active"
	OverrideStatusStale  OverrideStatus = "stale"
)

// BookSource 是 Legado 书源的关系化视图。header/rules 原样保存 JSONB，
// 其中 rules 保留书源 JSON 的整段原始内容（未知键透传）。
type BookSource struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	BookSourceURL   string
	BookSourceName  string
	BookSourceGroup string
	BookSourceType  int16
	Enabled         bool
	EnabledExplore  bool
	Weight          int32
	RespondTime     int32
	Charset         *string
	ConcurrentRate  *string
	Header          json.RawMessage
	LoginURL        *string
	LoginCheckJS    *string
	Rules           json.RawMessage
	FailureCount    int32
	LastCheckedAt   *time.Time
	LastSuccessAt   *time.Time
	Version         int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Book 是逻辑书（跨源唯一的用户书籍）。
type Book struct {
	ID                 uuid.UUID
	UserID             uuid.UUID
	BookType           BookType
	NormalizedName     string
	NormalizedAuthor   string
	Name               string
	Author             string
	CoverURL           *string
	CustomCoverURL     *string
	Intro              *string
	Kind               *string
	WordCount          *int32
	Status             BookStatus
	ActiveSourceID     *uuid.UUID
	ActiveBookURL      *string
	TotalChapters      int32
	LatestChapterTitle *string
	LatestChapterAt    *time.Time
	LastReadAt         *time.Time
	Version            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	// FileHash 本地书导入去重用的文件 SHA-256（在线书为 nil）。
	FileHash *string
}

// BookSourceMapping 记录一本书在某个书源下的绑定与目录快照状态。
type BookSourceMapping struct {
	ID                 uuid.UUID
	BookID             uuid.UUID
	SourceID           uuid.UUID
	BookURL            string
	TOCURL             *string
	Name               *string
	Author             *string
	CoverURL           *string
	LatestChapterTitle *string
	TotalChapters      int32
	SyncStatus         MappingStatus
	FailureCount       int32
	LastSyncedAt       *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Chapter 是目录中的一行；卷名行 is_volume=true 且 chapter_index=NULL。
type Chapter struct {
	ID              int64
	BookID          uuid.UUID
	SourceID        uuid.UUID
	ChapterIndex    *int32
	SortOrder       int32
	Title           string
	NormalizedTitle string
	URL             string
	URLHash         string
	IsVolume        bool
	IsVIP           bool
	WordCount       int32
	PublishedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ReadingProgress 是单用户单书的阅读进度。version 由应用层维护。
type ReadingProgress struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	BookID          uuid.UUID
	ChapterIndex    int32
	CharOffset      int32
	ChapterTitle    string
	NormalizedTitle string
	ChapterRatio    float64
	ProgressPercent float64
	ClientUpdatedAt time.Time
	ServerUpdatedAt time.Time
	Version         int64
	DeviceID        *string
}

// ChapterSourceOverride 是单个章节从其他书源拉取正文的覆盖记录。
type ChapterSourceOverride struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	BookID          uuid.UUID
	ChapterIndex    int32
	SourceID        uuid.UUID
	ChapterURL      string
	ChapterTitle    *string
	NormalizedTitle *string
	Status          OverrideStatus
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// BookshelfEntry 是 v_bookshelf 视图的一行。
type BookshelfEntry struct {
	ID                   uuid.UUID
	UserID               uuid.UUID
	Name                 string
	Author               string
	BookType             BookType
	Status               BookStatus
	CoverURL             *string
	TotalChapters        int32
	LatestChapterTitle   *string
	LastReadAt           *time.Time
	ActiveSourceID       *uuid.UUID
	ActiveBookURL        *string
	Version              int64
	CreatedAt            time.Time
	UpdatedAt            time.Time
	NeedsSource          bool
	OverrideCount        int32
	ActiveSourceName     *string
	ProgressChapterIndex *int32
	ProgressChapterTitle *string
	ProgressRatio        *float64
	ProgressPercent      *float64
	ProgressUpdatedAt    *time.Time
	ProgressCharOffset   *int32
	ProgressVersion      *int64
}
