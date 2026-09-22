package config

import "testing"

func TestDefaults(t *testing.T) {
	s, err := fromEnv([]string{})
	if err != nil {
		t.Fatalf("defaults failed validation: %v", err)
	}
	if s.EmbedSeed.Dim != 1024 || s.EmbedSeed.ModelID != "BAAI/bge-m3" || s.EmbedSeed.Provider != "tei" {
		t.Fatalf("embedding seed defaults drifted: %+v", s.EmbedSeed)
	}
	if s.ShardPages != 20 || s.OCRMinCharsPerPage != 20 || s.ParserRecycleAfter != 10 {
		t.Fatalf("parsing defaults drifted: %+v", s)
	}
	if s.MaxParseBacklog != 2000 || s.MaxDocumentPages != 800 {
		t.Fatalf("backpressure defaults drifted: %+v", s)
	}
	if s.SearchDefaultTopK != 8 || s.SearchMaxTopK != 25 || s.ReadPagesMax != 30 {
		t.Fatalf("mcp defaults drifted: %+v", s)
	}
	if s.MCPPort != 8430 || s.APIPort != 8000 {
		t.Fatalf("port defaults drifted: %+v", s)
	}
	if s.S3BucketRaw != "raw" || s.S3BucketParsed != "parsed" {
		t.Fatalf("bucket defaults drifted: %+v", s)
	}
	if s.MinYieldCharsPerPage != 50 {
		t.Fatalf("yield default drifted: %+v", s)
	}
	if s.JanitorInterval.Seconds() != 30 {
		t.Fatalf("janitor interval default drifted: %+v", s)
	}
}

func TestEnvOverrides(t *testing.T) {
	s, err := fromEnv([]string{
		"MAX_PARSE_BACKLOG=20000", // compose core override
		"EMBED_BACKEND=ollama",    // seed-only var
		"EMBED_DIM=768",           // seed-only var
		"SEARCH_MAX_TOP_K=25",
		"PARSER_RECYCLE_AFTER=5",
	})
	if err != nil {
		t.Fatalf("env load failed: %v", err)
	}
	if s.MaxParseBacklog != 20000 || s.EmbedSeed.Provider != "ollama" || s.EmbedSeed.Dim != 768 ||
		s.ParserRecycleAfter != 5 {
		t.Fatalf("env overrides not applied: %+v", s)
	}
}

// TestRetiredEmbedVarsNotRead proves the hard cut: the runtime embed knobs
// are gone from the struct — only the seed block carries the legacy values.
func TestRetiredEmbedVarsNotRead(t *testing.T) {
	s, err := fromEnv([]string{
		"EMBED_BATCH_SIZE=16",
		"EMBED_CTX_BUDGET=999",
		"EMBED_TRUNCATE_CHARS=99",
		"EMBED_QUERY_PREFIX=zz",
	})
	if err != nil {
		t.Fatalf("env load failed: %v", err)
	}
	// These fields no longer exist on Settings — compile-time proof. The
	// seed must be untouched by the retired runtime knobs:
	if s.EmbedSeed.Dim != 1024 {
		t.Fatalf("retired vars leaked into the seed: %+v", s.EmbedSeed)
	}
}

func TestValidation(t *testing.T) {
	// top_k ordering
	if _, err := fromEnv([]string{"SEARCH_DEFAULT_TOP_K=30", "SEARCH_MAX_TOP_K=25"}); err == nil {
		t.Fatal("expected validation error for default > max top_k")
	}
	// rerank requires url — env empties fall back to the 1.0 default URL,
	// so the invariant is exercised directly on the struct.
	s := DefaultSettings()
	s.RerankSeed.QueryURL = ""
	s.RerankSeed.Enabled = true
	if err := s.Validate(); err == nil {
		t.Fatal("expected validation error for rerank without url")
	}
}
