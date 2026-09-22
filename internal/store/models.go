package store

import "time"

// DocState is the document state machine (PRD §5); values are the exact
// strings the OpenAPI DocState enum carries.
type DocState string

const (
	StateUploaded  DocState = "uploaded"
	StateSplitting DocState = "splitting"
	StateParsing   DocState = "parsing"
	StateEmbedding DocState = "embedding"
	StateIndexing  DocState = "indexing"
	StateReady     DocState = "ready"
	StateFailed    DocState = "failed"
	StatePartial   DocState = "partial"
	StateArchived  DocState = "archived"
)

// ShardState is the shard lifecycle; skipped marks a retry-ladder parent
// replaced by sub-shards.
type ShardState string

const (
	ShardPending ShardState = "pending"
	ShardRunning ShardState = "running"
	ShardDone    ShardState = "done"
	ShardFailed  ShardState = "failed"
	ShardSkipped ShardState = "skipped"
)

// Document mirrors one `documents` row (OpenAPI DocumentOut fields).
type Document struct {
	ID            string
	CollectionID  *string
	Title         *string
	Author        *string
	Filename      *string
	ByteSize      *int64
	SourceURI     string
	ContentSHA256 string
	PageCount     *int
	MimeType      *string
	State         DocState
	ErrorCode     *string
	ErrorDetail   *string
	TotalShards   *int
	ShardsDone    int
	ShardsFailed  int
	ChunkCount    *int
	Completeness  *float64
	Metadata      map[string]any
	UploadedBy    *string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ReadyAt       *time.Time
}

// Shard mirrors one `shards` row (OpenAPI ShardOut fields).
type Shard struct {
	DocID            string
	Idx              int
	PageStart        int
	PageEnd          int
	State            ShardState
	Attempts         int
	NeedsOCR         bool
	MeanCharsPerPage *float64
	ParsedURI        *string
	WorkerID         *string
	LeaseUntil       *time.Time
	DurationMS       *int
	PeakRSSMB        *int
	DoneAt           *time.Time
	ErrorCode        *string
	ErrorDetail      *string
	CreatedAt        time.Time
}

// Chunk mirrors one `chunks` row; Parents are breadcrumb carriers without
// vectors (is_parent=true), Children carry the embedding.
type Chunk struct {
	ID               string
	DocID            string
	CollectionID     string
	ChunkHash        string
	ParentID         *string
	IsParent         bool
	Seq              int
	PageStart        *int
	PageEnd          *int
	HeadingPath      []string
	HeaderBreadcrumb *string
	Text             string
	TokenCount       int
	EmbeddedAt       *time.Time
	CreatedAt        time.Time
}

// Collection mirrors one `collections` row.
type Collection struct {
	ID               string
	Name             string
	EmbeddingModel   string
	VectorDim        int
	EmbeddingModelID *string
	CreatedAt        time.Time
}

// EmbeddingModel mirrors one `embedding_models` row (the model registry).
// The api_key column is deliberately NOT a field — it is write-only, never
// serialized out; HasAPIKey carries its presence for the UI.
type EmbeddingModel struct {
	ID            string
	Name          string
	Provider      string // tei | ollama | openai
	ModelID       string
	IngestURL     string
	QueryURL      string
	HasAPIKey     bool
	VectorDim     int
	QueryPrefix   string
	BatchSize     int
	CtxBudget     int
	TruncateChars int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ApiKey mirrors one `api_keys` row. Validity rule: revoked_at set, or
// expires_at in the past, both mean 401 (deps.py/auth.py parity).
type ApiKey struct {
	ID          string
	Name        *string
	KeyHash     string
	Scopes      []string
	Collections []string
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

// Event mirrors one `events` row (the admin UI's Logs page).
type Event struct {
	ID        int64
	DocID     *string
	ShardIdx  *int
	Level     string
	Stage     *string
	Code      *string
	Message   string
	Context   map[string]any
	WorkerID  *string
	CreatedAt time.Time
}

// KeyUsage mirrors one `key_usage` row.
type KeyUsage struct {
	ID        int64
	ApiKeyID  string
	Surface   string // 'mcp' | 'api'
	Action    string
	CreatedAt time.Time
}

// MetricsRollup mirrors one `metrics_rollup` row (one per minute bucket).
type MetricsRollup struct {
	Bucket         time.Time
	PagesParsed    *int
	ShardsDone     *int
	ShardsFailed   *int
	ChunksEmbedded *int
	ParseP50MS     *int
	ParseP95MS     *int
	PeakRSSP95MB   *int
	QueueDepth     map[string]any
	SearchP95MS    *int
	SearchCount    *int
}
