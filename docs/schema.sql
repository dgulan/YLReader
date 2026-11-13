-- =============================================================================
-- YReader (Clean-Room Reader) — PostgreSQL 权威 DDL
-- 状态：Frozen v1.5（契约设计冻结；发布验收未完成，见 contract-freeze-v1.5.md）
--
-- 设计约束（与 docs/architecture.md 一一对应）：
--   * PostgreSQL 16+；所有时间列使用 timestamptz（UTC 存储）。
--   * 业务实体一律关系化；只有「书源解析规则」「HTTP 头」「任务载荷」等
--     开放结构进 jsonb。
--   * 多租户隔离以 user_id 为前缀列，所有业务查询必须带 user_id 条件；
--     Stage 4 可在此之上追加 RLS。
--   * 章节标题归一化（normalized_title）与书籍名归一化（normalized_name）
--     由 Go 服务写入，SQL 侧仅提供「兜底/回填」函数，避免两套语义漂移。
--   * 本文件不包含业务种子；ADR-017 迁移账本与基线登记供新库初始化。
--
-- ⚠️ JSONB + GIN 使用契约（违反即视为契约缺陷，详见 §JSONB 契约注释）：
--   book_sources.rules 上的 GIN(jsonb_ops) 只服务 @>、?、?|、?& 四类谓词。
--   严禁 `WHERE rules->>'search' = '...'` 或 `rules->'content'->>'content' LIKE ...`
--   并指望该索引生效——这类查询必须走表达式索引/生成列，或退化为按 id 点查。
-- =============================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS schema_migrations (
  version bigint PRIMARY KEY,
  name text NOT NULL,
  checksum char(64) NOT NULL,
  applied_at timestamptz NOT NULL DEFAULT now()
);

-- -----------------------------------------------------------------------------
-- 0. 扩展与类型
-- -----------------------------------------------------------------------------
CREATE EXTENSION IF NOT EXISTS pgcrypto;   -- gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pg_trgm;    -- 章节标题模糊匹配（换源三级定位）

DO $$
BEGIN
  CREATE TYPE user_role AS ENUM ('admin', 'user');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
  CREATE TYPE book_type AS ENUM ('online', 'local');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
  CREATE TYPE book_status AS ENUM ('unknown', 'serializing', 'completed', 'paused');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- 单本书在某个书源下的映射健康度
DO $$
BEGIN
  CREATE TYPE mapping_status AS ENUM ('pending', 'active', 'stale', 'broken');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- 书源失败归因：网络 / 超时 / 规则逻辑 / 被拦截 / 不支持 / 鉴权
DO $$
BEGIN
  CREATE TYPE failure_kind AS ENUM ('network', 'timeout', 'rule', 'blocked', 'unsupported', 'auth');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
  CREATE TYPE job_type AS ENUM (
  'toc_refresh',      -- 目录刷新/追更巡检
  'chapter_prefetch', -- 相邻章节预取
  'full_cache',       -- 整本缓存
  'source_health',    -- 书源健康检查
  'import_sources'    -- 旧版 Reader 数据/书源包导入
);
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

DO $$
BEGIN
  CREATE TYPE job_status AS ENUM ('queued', 'running', 'succeeded', 'failed', 'canceled');
EXCEPTION WHEN duplicate_object THEN NULL;
END $$;

-- -----------------------------------------------------------------------------
-- 1. 通用触发器函数
-- -----------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.updated_at := now();
  RETURN NEW;
END;
$$;

-- 乐观锁：应用层不带 version 条件时也会自增，配合 If-Match / If-Unmodified-Since
CREATE OR REPLACE FUNCTION bump_row_version() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.version := OLD.version + 1;
  RETURN NEW;
END;
$$;

-- 书籍名/作者归一化兜底：NFKC 由 Go 侧先行处理，这里只做「去空白 + 去标点 + 小写」。
-- 仅当应用未传 normalized_* 时由触发器调用，保证 NOT NULL 与唯一键可用。
CREATE OR REPLACE FUNCTION normalize_book_key(p_text text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
  SELECT lower(regexp_replace(
           coalesce(p_text, ''),
           '[[:space:][:punct:]，。、；：？！“”‘’（）《》【】〈〉「」『』…—～·　]',
           '', 'g'))
$$;

-- 章节标题归一化「兜底」实现：抽第一个阿拉伯数字（0-999999）→ 'n:<int>[:<clean_title>]'；
-- 抽不到则退化为 normalize_book_key。中文数字（一/二/…）与「第N回」变体
-- 由 Go 的 internal/engine/textnorm 负责，SQL 版只用于运维回填与索引兜底。
CREATE OR REPLACE FUNCTION chapter_title_key_fallback(p_title text) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
  -- 三段式：'n:<int>:<clean_title>'；无 clean_title 时为 'n:<int>'；无序号时为纯归一化文本。
  -- clean_title = 去掉「前缀 + 序号 + 章/节/回/话/卷/篇/集 后缀」后按 normalize_book_key 归一化。
  SELECT CASE
           WHEN num.n IS NOT NULL
             THEN 'n:' || num.n::int::text
                  || CASE WHEN cln.clean <> '' THEN ':' || cln.clean ELSE '' END
           ELSE normalize_book_key(p_title)
         END
  FROM (SELECT substring(coalesce(p_title, '') FROM '([0-9]{1,6})') AS n) num
  CROSS JOIN LATERAL (
    SELECT normalize_book_key(regexp_replace(
             coalesce(p_title, ''),
             '^[^0-9]*[0-9]{1,6}[[:space:]]*(章|节|回|话|卷|篇|集)?', ''
           )) AS clean
  ) cln
$$;

-- -----------------------------------------------------------------------------
-- 2. 用户与会话
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  username        text NOT NULL CHECK (char_length(username) BETWEEN 1 AND 64),
  password_hash   text NOT NULL,
  role            user_role NOT NULL DEFAULT 'user',
  display_name    text,
  token_version   integer NOT NULL DEFAULT 0 CHECK (token_version >= 0),
  is_disabled     boolean NOT NULL DEFAULT false,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  last_login_at   timestamptz
);
-- 用户名大小写不敏感唯一
CREATE UNIQUE INDEX IF NOT EXISTS ux_users_username_lower ON users (lower(username));
COMMENT ON COLUMN users.token_version IS '改密/踢下线时自增；JWT 校验必须比对，旧 token 立即失效。';

CREATE TABLE IF NOT EXISTS auth_sessions (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  refresh_token_hash  text NOT NULL UNIQUE,   -- 仅存 SHA-256，不落明文
  user_agent          text,
  ip                  inet,
  expires_at          timestamptz NOT NULL,
  revoked_at          timestamptz,
  created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_auth_sessions_user_active ON auth_sessions (user_id, expires_at DESC)
  WHERE revoked_at IS NULL;

-- 每用户键值设置（阅读器偏好、同步开关等），避免为每个 UI 开关加列
CREATE TABLE IF NOT EXISTS app_settings (
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key         text NOT NULL CHECK (key ~ '^[a-z0-9_.]{1,64}$'),
  value       jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, key)
);

-- -----------------------------------------------------------------------------
-- 3. 书源（Legado 3.0 兼容）
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS book_sources (
  id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id             uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  -- 检索/排序列：从书源 JSON 中提升为标准关系列
  book_source_url     text NOT NULL,
  book_source_name    text NOT NULL,
  book_source_group   text NOT NULL DEFAULT '默认',
  book_source_type    smallint NOT NULL DEFAULT 0 CHECK (book_source_type BETWEEN 0 AND 3),
  enabled             boolean NOT NULL DEFAULT true,
  enabled_explore     boolean NOT NULL DEFAULT true,
  weight              integer NOT NULL DEFAULT 0,
  respond_time        integer NOT NULL DEFAULT 0 CHECK (respond_time >= 0),  -- 观测到的平均耗时 ms
  charset             text,
  concurrent_rate     text,          -- 原样保留 Legado 的 "n/ms" 或纯数字
  header              jsonb NOT NULL DEFAULT '{}'::jsonb,
  login_url           text,
  login_check_js      text,

  -- 复杂解析规则整段 JSONB：保留 Legado 书源 JSON 的原始键名（未知键透传）
  rules               jsonb NOT NULL DEFAULT '{}'::jsonb,

  -- 健康度与运维
  failure_count       integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  last_checked_at     timestamptz,
  last_success_at     timestamptz,

  version             bigint NOT NULL DEFAULT 1 CHECK (version >= 1),

  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT uq_book_sources_user_url UNIQUE (user_id, book_source_url),
  CONSTRAINT ck_book_sources_url_scheme CHECK (book_source_url ~ '^(https?|local)://'),
  CONSTRAINT ck_book_sources_rules_object CHECK (jsonb_typeof(rules) = 'object')
);

-- 列表/检索主路径：按用户过滤 + 启用态 + 权重排序
CREATE INDEX IF NOT EXISTS ix_book_sources_user_enabled_weight
  ON book_sources (user_id, enabled, weight DESC, book_source_name);

CREATE INDEX IF NOT EXISTS ix_book_sources_group ON book_sources (user_id, book_source_group);

-- 默认 GIN：jsonb_ops（支持 @> / ? / ?| / ?&；jsonb_path_ops 不支持键存在性判断）
CREATE INDEX IF NOT EXISTS ix_book_sources_rules_gin ON book_sources USING gin (rules jsonb_ops);

-- 书源名模糊搜索（q 参数）走 trigram
CREATE INDEX IF NOT EXISTS ix_book_sources_name_trgm ON book_sources USING gin (book_source_name gin_trgm_ops);

COMMENT ON COLUMN book_sources.rules IS
'Legado 3.0 书源规则整段 JSONB（search/explore/bookInfo/toc/content 及扩展键）。
 索引契约：ix_book_sources_rules_gin 仅覆盖 @>、?、?|、?&；禁止对 rules->>''x'' 做等值/LIKE 并期望索引生效。
 需要按规则内部字段检索时：建表达式索引或生成列，且必须附带 user_id 条件。';

COMMENT ON COLUMN book_sources.header IS
'HTTP 请求头。v1.4（ADR-010）起敏感值以应用层 AES-256-GCM 信封加密存储：
 密文形如 {"__enc":"v1:<kid>:<base64(nonce||ciphertext||tag)>"}，AAD = user_id|source_id，nonce 12B 随机。
 禁止对 header 做键存在性/路径提取查询（如 header ? ''Cookie'' / header->>''Cookie''）：密文不可索引，且会绕过解密路径；
 所有读取必须经 SourceRepository 透明解密，engine/fetcher 不得直接持有 *pgxpool.Pool 读该列。';
COMMENT ON COLUMN book_sources.concurrent_rate IS
'Legado concurrentRate 原值：纯数字=并发上限；"n/ms"=每 ms 毫秒窗口 n 次。';

-- -----------------------------------------------------------------------------
-- 1.x 存量库兼容（幂等 DO 块；新建库上同样安全）
--   * book_source_type 取值范围放宽到 0~3（3 = 本地书库虚拟源）
--   * book_source_url 允许 local:// scheme（本地书库虚拟源）
-- -----------------------------------------------------------------------------
DO $$
DECLARE r RECORD;
BEGIN
  IF to_regclass('book_sources') IS NOT NULL THEN
    FOR r IN (
      SELECT conname FROM pg_constraint
      WHERE conrelid = 'book_sources'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%book_source_type%'
    ) LOOP
      EXECUTE 'ALTER TABLE book_sources DROP CONSTRAINT IF EXISTS ' || quote_ident(r.conname);
    END LOOP;
  END IF;
END $$;
ALTER TABLE book_sources ADD CONSTRAINT ck_book_sources_type CHECK (book_source_type BETWEEN 0 AND 3);

DO $$
DECLARE r RECORD;
BEGIN
  IF to_regclass('book_sources') IS NOT NULL THEN
    FOR r IN (
      SELECT conname FROM pg_constraint
      WHERE conrelid = 'book_sources'::regclass AND contype = 'c'
        AND pg_get_constraintdef(oid) LIKE '%book_source_url%'
        AND pg_get_constraintdef(oid) LIKE '%https?%'
    ) LOOP
      EXECUTE 'ALTER TABLE book_sources DROP CONSTRAINT IF EXISTS ' || quote_ident(r.conname);
    END LOOP;
  END IF;
END $$;
ALTER TABLE book_sources ADD CONSTRAINT ck_book_sources_url_scheme CHECK (book_source_url ~ '^(https?|local)://');

-- 失败/熔断记录：expires_at 驱动「书源失效 TTL」，替代硬编码 600s
CREATE TABLE IF NOT EXISTS source_failures (
  id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source_id    uuid NOT NULL REFERENCES book_sources(id) ON DELETE CASCADE,
  kind         failure_kind NOT NULL,
  error_code   text NOT NULL,
  message      text NOT NULL DEFAULT '',
  occurred_at  timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL,
  resolved_at  timestamptz,
  CONSTRAINT ck_source_failures_ttl CHECK (expires_at > occurred_at)
);
CREATE INDEX IF NOT EXISTS ix_source_failures_active
  ON source_failures (source_id, expires_at DESC)
  WHERE resolved_at IS NULL;

-- -----------------------------------------------------------------------------
-- 4. 书籍（逻辑书唯一 + 多源绑定）
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS books (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id               uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_type             book_type NOT NULL DEFAULT 'online',

  -- 逻辑书唯一键：同一用户下「归一化书名 + 归一化作者」只允许一本书
  normalized_name       text NOT NULL,
  normalized_author     text NOT NULL DEFAULT '',
  name                  text NOT NULL,
  author                text NOT NULL DEFAULT '',

  -- 展示与元数据
  cover_url             text,
  custom_cover_url      text,
  intro                 text,
  kind                  text,
  word_count            integer CHECK (word_count IS NULL OR word_count >= 0),
  status                book_status NOT NULL DEFAULT 'unknown',

  -- 当前激活书源（换源只改这两个字段，不新建书）
  active_source_id      uuid REFERENCES book_sources(id) ON DELETE SET NULL,
  active_book_url       text,

  total_chapters        integer NOT NULL DEFAULT 0 CHECK (total_chapters >= 0),
  latest_chapter_title  text,
  latest_chapter_at     timestamptz,

  last_read_at          timestamptz,
  -- 本地书导入去重：文件 SHA-256（在线书为 NULL）
  file_hash             text,
  version               bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT uq_books_user_name_author UNIQUE (user_id, normalized_name, normalized_author),
  CONSTRAINT ck_books_active_source_url_pair CHECK ((active_source_id IS NULL) = (active_book_url IS NULL))
  -- 注意：不强制 online 书必须有 active_source_id。
  -- 删除书源时 FK 为 ON DELETE SET NULL，书会进入「待换源」状态（active_source_id IS NULL），
  -- 由 service 层在书架列表标记 needs_source=true；若在此加强制约束，删除书源将永远失败。
);

CREATE INDEX IF NOT EXISTS ix_books_user_last_read ON books (user_id, last_read_at DESC NULLS LAST);
CREATE INDEX IF NOT EXISTS ix_books_user_name_trgm ON books USING gin (name gin_trgm_ops);

-- 本地书导入幂等：同一用户同一文件只允许一本书（部分唯一索引，在线书 file_hash 为 NULL）
-- 存量库（v1.1 及更早）补列：新建库上为 no-op。
ALTER TABLE books ADD COLUMN IF NOT EXISTS file_hash text;
CREATE UNIQUE INDEX IF NOT EXISTS uq_books_user_file_hash
  ON books (user_id, file_hash)
  WHERE file_hash IS NOT NULL;

-- 同一逻辑书在各备用书源下的绑定关系与同步状态
CREATE TABLE IF NOT EXISTS book_source_mappings (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  book_id               uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  source_id             uuid NOT NULL REFERENCES book_sources(id) ON DELETE CASCADE,
  book_url              text NOT NULL,
  toc_url               text,
  name                  text,
  author                text,
  cover_url             text,
  latest_chapter_title  text,
  total_chapters        integer NOT NULL DEFAULT 0 CHECK (total_chapters >= 0),
  sync_status           mapping_status NOT NULL DEFAULT 'pending',
  failure_count         integer NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  last_synced_at        timestamptz,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT uq_mappings_book_source_url UNIQUE (book_id, source_id, book_url)
);
CREATE INDEX IF NOT EXISTS ix_mappings_book_status ON book_source_mappings (book_id, sync_status);
CREATE INDEX IF NOT EXISTS ix_mappings_source ON book_source_mappings (source_id, book_url);
-- 同一 (book, source) 允许多条 URL（源站换域名），但同时只能有一条 active
CREATE UNIQUE INDEX IF NOT EXISTS uq_mappings_one_active
  ON book_source_mappings (book_id, source_id)
  WHERE sync_status = 'active';

-- 章节：每个 (book, source) 一套目录
--   chapter_index：仅可读章节编号（0..N-1）；卷名行为 NULL
--   sort_order   ：目录展示顺序（含卷名），用于 TOC 原样渲染
CREATE TABLE IF NOT EXISTS chapters (
  id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  book_id           uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  source_id         uuid NOT NULL REFERENCES book_sources(id) ON DELETE CASCADE,

  chapter_index     integer CHECK (chapter_index IS NULL OR chapter_index >= 0),
  sort_order        integer NOT NULL CHECK (sort_order >= 0),
  title             text NOT NULL,
  normalized_title  text NOT NULL,
  url               text NOT NULL,
  url_hash          text NOT NULL,          -- sha256(absolute_url)；卷名行用合成键 "volume:<sort_order>"
  is_volume         boolean NOT NULL DEFAULT false,
  is_vip            boolean NOT NULL DEFAULT false,
  word_count        integer NOT NULL DEFAULT 0 CHECK (word_count >= 0),
  published_at      timestamptz,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT ck_chapters_volume_index
    CHECK ((is_volume AND chapter_index IS NULL) OR (NOT is_volume AND chapter_index IS NOT NULL)),
  CONSTRAINT uq_chapters_book_source_url UNIQUE (book_id, source_id, url_hash)
);
-- 只有可读章节参与 0..N-1 唯一编号
CREATE UNIQUE INDEX IF NOT EXISTS uq_chapters_book_source_index
  ON chapters (book_id, source_id, chapter_index)
  WHERE chapter_index IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_chapters_book_source_sort ON chapters (book_id, source_id, sort_order);
CREATE INDEX IF NOT EXISTS ix_chapters_book_source_title ON chapters (book_id, source_id, normalized_title);
-- 换源「模糊匹配」级别必须用 % 前置谓词走 trigram GIN；similarity() 仅做二次过滤（ADR-014）
CREATE INDEX IF NOT EXISTS ix_chapters_title_trgm ON chapters USING gin (normalized_title gin_trgm_ops);

COMMENT ON COLUMN chapters.normalized_title IS
'章节标题归一化键，由 Go engine/textnorm 生成（支持中文数字与「第N回」变体）：
 有阿拉伯/中文数字章节号 → "n:<数字>"；否则为去空白去标点小写标题。
 换源重定位 Level 1 用它精确匹配；Level 2 用 % 前置谓词（pg_trgm GIN 可服务）
 AND similarity(...) >= p_threshold 二次过滤（ADR-014）。';

-- -----------------------------------------------------------------------------
-- 5. 阅读进度（多端冲突 + 换源重定位）
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reading_progress (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id            uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id            uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,

  chapter_index      integer NOT NULL CHECK (chapter_index >= 0),
  char_offset        integer NOT NULL DEFAULT 0 CHECK (char_offset >= 0),
  -- 冗余章节标识：换源后当前章节可能在新源不存在，必须靠标题+比例重定位
  chapter_title      text NOT NULL DEFAULT '',
  normalized_title   text NOT NULL DEFAULT '',
  chapter_ratio      numeric(6,5) NOT NULL DEFAULT 0 CHECK (chapter_ratio >= 0 AND chapter_ratio <= 1),
  progress_percent   numeric(5,2) NOT NULL DEFAULT 0 CHECK (progress_percent >= 0 AND progress_percent <= 100),

  -- 冲突解决：客户端时间只作参考，服务端以 version 做乐观锁
  client_updated_at  timestamptz NOT NULL DEFAULT now(),
  server_updated_at  timestamptz NOT NULL DEFAULT now(),
  version            bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  device_id          text,

  CONSTRAINT uq_progress_user_book UNIQUE (user_id, book_id)
);
CREATE INDEX IF NOT EXISTS ix_progress_user_updated ON reading_progress (user_id, server_updated_at DESC);

-- 只维护 server_updated_at；version 由应用层显式原子自增（见 architecture.md §4.2.1）
CREATE OR REPLACE FUNCTION touch_progress_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.server_updated_at := now();
  RETURN NEW;
END;
$$;

CREATE OR REPLACE TRIGGER trg_progress_touch_updated_at
  BEFORE UPDATE ON reading_progress
  FOR EACH ROW EXECUTE FUNCTION touch_progress_updated_at();

-- -----------------------------------------------------------------------------
-- 章节级源覆盖：单章从其他书源拉取正文，不影响 books.active_source_id
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS chapter_source_overrides (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id          uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  chapter_index    integer NOT NULL CHECK (chapter_index >= 0),
  source_id        uuid NOT NULL REFERENCES book_sources(id) ON DELETE CASCADE,
  chapter_url      text NOT NULL CHECK (chapter_url ~ '^https?://'),
  chapter_title    text,
  normalized_title text,
  status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active','stale')),
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_chapter_overrides_book_index UNIQUE (book_id, chapter_index)
);
CREATE INDEX IF NOT EXISTS ix_chapter_overrides_user_book ON chapter_source_overrides (user_id, book_id);
CREATE INDEX IF NOT EXISTS ix_chapter_overrides_source ON chapter_source_overrides (source_id);
CREATE OR REPLACE TRIGGER trg_chapter_overrides_updated BEFORE UPDATE ON chapter_source_overrides
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE chapter_source_overrides IS
'章节级源覆盖：单章从其他书源拉取正文，不影响 books.active_source_id。
 chapter_index 语义：始终以 books.active_source_id 下 chapters 的编号为准，不随 override 变化。
 整本书换源时清空该书所有 override；追更与目录刷新不触碰 override；
 目录刷新导致章节数变少、chapter_index 越界时标记 status=stale';

-- -----------------------------------------------------------------------------
-- 6. 书签 / 替换净化 / 章节缓存 / 异步任务
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS bookmarks (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  book_id        uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE,
  chapter_index  integer NOT NULL CHECK (chapter_index >= 0),
  char_offset    integer NOT NULL DEFAULT 0 CHECK (char_offset >= 0),
  chapter_title  text NOT NULL DEFAULT '',
  excerpt        text,
  note           text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_bookmarks_user_book_chapter_offset UNIQUE (user_id, book_id, chapter_index, char_offset)
);
CREATE INDEX IF NOT EXISTS ix_bookmarks_book_chapter ON bookmarks (book_id, chapter_index);

CREATE TABLE IF NOT EXISTS replace_rules (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name          text NOT NULL,
  group_name    text NOT NULL DEFAULT '默认',
  pattern       text NOT NULL,
  replacement   text NOT NULL DEFAULT '',
  scope         text NOT NULL DEFAULT 'content' CHECK (scope IN ('content', 'title', 'all')),
  is_enabled    boolean NOT NULL DEFAULT true,
  order_index   integer NOT NULL DEFAULT 0,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_replace_rules_user_order ON replace_rules (user_id, is_enabled, order_index);

-- ADR-013 Scheme A:
-- chapter_cache is deprecated by the architecture decision.
-- This general-purpose schema does not perform physical removal.
-- Physical removal requires a separately authorized A2 migration.
-- ADR-015 review: this boundary is unchanged; see docs/v1.5-contract-review.md.

CREATE TABLE IF NOT EXISTS jobs (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  type           job_type NOT NULL,
  status         job_status NOT NULL DEFAULT 'queued',
  payload        jsonb NOT NULL DEFAULT '{}'::jsonb,
  result         jsonb,
  progress       smallint NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
  error_code     text,
  error_message  text,
  attempts       smallint NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  created_at     timestamptz NOT NULL DEFAULT now(),
  started_at     timestamptz,
  finished_at    timestamptz,
  expires_at     timestamptz,
  worker_id      text
);
CREATE INDEX IF NOT EXISTS ix_jobs_user_status ON jobs (user_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_jobs_run_queue ON jobs (created_at) WHERE status = 'queued';

-- v1.3（M3c）：存量库补列 + Worker 租约隔离索引（幂等，可重复执行）。
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS worker_id text;
CREATE INDEX IF NOT EXISTS ix_jobs_worker_status ON jobs(worker_id, status) WHERE status = 'running';

-- v1.4（S5-2）：payload.source_id 为 full_cache 的预热目标源；target_source_id 由 payload 解析，不落库。

-- -----------------------------------------------------------------------------
-- 7. 视图：书架列表（书籍 + 进度 + 激活书源）
-- -----------------------------------------------------------------------------
CREATE OR REPLACE VIEW v_bookshelf AS
SELECT
  b.id,
  b.user_id,
  b.name,
  b.author,
  b.book_type,
  b.status,
  COALESCE(b.custom_cover_url, b.cover_url) AS cover_url,
  b.total_chapters,
  b.latest_chapter_title,
  b.last_read_at,
  b.active_source_id,
  b.active_book_url,
  b.version,
  b.created_at,
  b.updated_at,
  (b.book_type = 'online' AND b.active_source_id IS NULL) AS needs_source,
  (SELECT count(*)::integer
     FROM chapter_source_overrides o
    WHERE o.user_id = b.user_id
      AND o.book_id = b.id
      AND o.status = 'active') AS override_count,
  s.book_source_name AS active_source_name,
  p.chapter_index    AS progress_chapter_index,
  p.chapter_title    AS progress_chapter_title,
  p.chapter_ratio    AS progress_ratio,
  p.progress_percent,
  p.server_updated_at AS progress_updated_at,
  p.char_offset AS progress_char_offset,
  p.version AS progress_version
FROM books b
LEFT JOIN book_sources s ON s.id = b.active_source_id
LEFT JOIN reading_progress p ON p.book_id = b.id AND p.user_id = b.user_id;

-- -----------------------------------------------------------------------------
-- 8. updated_at 触发器
-- -----------------------------------------------------------------------------
CREATE OR REPLACE TRIGGER trg_users_updated      BEFORE UPDATE ON users      FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_sources_updated    BEFORE UPDATE ON book_sources FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_books_updated      BEFORE UPDATE ON books      FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_mappings_updated   BEFORE UPDATE ON book_source_mappings FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_chapters_updated   BEFORE UPDATE ON chapters   FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE TRIGGER trg_replace_updated    BEFORE UPDATE ON replace_rules FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE OR REPLACE TRIGGER trg_sources_version    BEFORE UPDATE ON book_sources FOR EACH ROW EXECUTE FUNCTION bump_row_version();
CREATE OR REPLACE TRIGGER trg_books_version      BEFORE UPDATE ON books      FOR EACH ROW EXECUTE FUNCTION bump_row_version();

-- books：应用未提供归一化键时兜底填充（不覆盖应用传入值）
CREATE OR REPLACE FUNCTION books_fill_normalized_keys() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.normalized_name IS NULL OR NEW.normalized_name = '' THEN
    NEW.normalized_name := normalize_book_key(NEW.name);
  END IF;
  IF NEW.normalized_author IS NULL THEN
    -- 与 Go pkg/textnorm.NormalizeAuthor 对齐：先剥「作者[:：]」前缀与末尾「著」再归一化
    NEW.normalized_author := normalize_book_key(
      regexp_replace(
        regexp_replace(coalesce(NEW.author, ''), '^(?:作者|著)[[:space:]]*[:：]?', ''),
        '[[:space:]]*著[[:space:]]*$', ''
      )
    );
  END IF;
  RETURN NEW;
END;
$$;

CREATE OR REPLACE TRIGGER trg_books_fill_keys
  BEFORE INSERT OR UPDATE ON books
  FOR EACH ROW EXECUTE FUNCTION books_fill_normalized_keys();

-- -----------------------------------------------------------------------------
-- 9. 维护辅助：换源三级重定位所需的相似度查询（只读函数，供 repository 调用）
-- -----------------------------------------------------------------------------
-- Level 1：邻近窗口内按 normalized_title 精确匹配（优先返回离旧 index 最近的）
CREATE OR REPLACE FUNCTION relocate_exact(
  p_book_id uuid, p_source_id uuid, p_normalized_title text,
  p_old_index integer, p_window integer DEFAULT 20
) RETURNS TABLE (chapter_index integer, title text, distance integer)
LANGUAGE sql STABLE AS $$
  SELECT c.chapter_index, c.title, abs(c.chapter_index - p_old_index) AS distance
  FROM chapters c
  WHERE c.book_id = p_book_id
    AND c.source_id = p_source_id
    AND c.is_volume = false
    AND c.normalized_title = p_normalized_title
    AND c.chapter_index BETWEEN greatest(0, p_old_index - p_window)
                            AND p_old_index + p_window
  ORDER BY distance ASC, c.chapter_index ASC
  LIMIT 1
$$;

-- Level 2：窗口内 trigram 相似度模糊匹配
-- v1.5（DEF-CODE-11）：先用 % 运算符做索引谓词（pg_trgm GIN 可服务），
-- 再用 similarity(...) >= p_threshold 二次过滤保证精度。
-- % 的判定阈值来自 pg_trgm.similarity_threshold（默认 0.3，是相似度下限），
-- 因此 % 结果集 ⊇ similarity >= p_threshold 结果集（p_threshold=0.6 时），语义等价。
-- 语义等价硬约束：对每次调用，运行环境必须保证 pg_trgm.similarity_threshold <= p_threshold。
-- 当前生产调用固定 p_threshold = 0.6（默认 GUC 0.3 <= 0.6）；若调用者传入更低阈值，
-- 必须同步确认 GUC 不高于该值，否则 % 前置谓词会提前截断候选集破坏等价性。
CREATE OR REPLACE FUNCTION relocate_fuzzy(
  p_book_id uuid, p_source_id uuid, p_normalized_title text,
  p_old_index integer, p_window integer DEFAULT 100, p_threshold real DEFAULT 0.6
) RETURNS TABLE (chapter_index integer, title text, similarity real)
LANGUAGE sql STABLE AS $$
  SELECT c.chapter_index, c.title,
         similarity(c.normalized_title, p_normalized_title) AS sim
  FROM chapters c
  WHERE c.book_id = p_book_id
    AND c.source_id = p_source_id
    AND c.is_volume = false
    AND c.chapter_index BETWEEN greatest(0, p_old_index - p_window)
                            AND p_old_index + p_window
    AND c.normalized_title % p_normalized_title
    AND similarity(c.normalized_title, p_normalized_title) >= p_threshold
  ORDER BY sim DESC, abs(c.chapter_index - p_old_index) ASC
  LIMIT 1
$$;

-- ADR-017 baseline registration (excluded from checksum).
-- ADR-018: import budget revision changes no DDL or published migration checksum.
INSERT INTO schema_migrations(version,name,checksum) VALUES(1,'baseline_v1_5','7e275f0f5dc37fa7b0834bd3d80da7c192f7bc7b854041f2f1e99b0cae3c3e51') ON CONFLICT(version) DO NOTHING;

COMMIT;
