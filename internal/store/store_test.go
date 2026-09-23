package store

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

func mustDB(t *testing.T, image string) *DB {
	t.Helper()
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			Env:          map[string]string{"POSTGRES_USER": "rag", "POSTGRES_PASSWORD": "rag", "POSTGRES_DB": "rag"},
			ExposedPorts: []string{"5432/tcp"},
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
	host, _ := ctr.Host(ctx)
	port, _ := ctr.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://rag:rag@%s:%s/rag?sslmode=disable", host, port.Port())
	db, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func TestSchemaBootstrapIdempotent(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	// bootstrap runs once inside NewPool; a second application must be a
	// no-op (IF NOT EXISTS everywhere + guarded bm25 call).
	if err := db.BootstrapSchema(ctx); err != nil {
		t.Fatalf("second bootstrap must be idempotent: %v", err)
	}
}

func TestClaimShardAtomicity(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	if err := db.Tx(ctx, func(tx txType) error {
		_, err := db.InsertShards(ctx, tx, doc, [][2]int{{1, 20}}, 0)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// two concurrent claims → one winner
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := db.Tx(ctx, func(tx txType) error {
				shard, err := db.ClaimShard(ctx, tx, doc, 0, fmt.Sprintf("w%d", i), 600)
				if err != nil {
					return err
				}
				if shard != nil {
					winners.Add(1)
				}
				return nil
			})
			if err != nil {
				t.Errorf("claim tx: %v", err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("atomic claim broken: %d winners", winners.Load())
	}
}

func TestFindDuplicateAndBookSettled(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)

	dup, err := db.FindDuplicate(ctx, "books", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if dup == nil {
		t.Fatal("expected duplicate hit")
	}

	d, err := db.GetDocument(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if BookSettled(d) {
		t.Fatal("zero-shard doc must not be settled")
	}
	total := 2
	if err := db.Tx(ctx, func(tx txType) error {
		_, err := db.Pool.Exec(ctx, `UPDATE documents SET total_shards = $2, shards_done = 2 WHERE id = $1`, doc, total)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	d, _ = db.GetDocument(ctx, doc)
	if !BookSettled(d) {
		t.Fatal("settled book must read true")
	}
}

func TestChunkDedupeUniqueConstraint(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	chunk := ChildChunk{Seq: 0, ChunkHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Text: "body", TokenCount: 2}
	err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{chunk}); err != nil {
			return err
		}
		// second insert of the same hash: ON CONFLICT DO NOTHING must not raise
		return db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{chunk})
	})
	if err != nil {
		t.Fatalf("re-delivered insert must be conflict-safe: %v", err)
	}
	n, err := db.CountDocChunks(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dedupe drift: %d chunks", n)
	}
}

// TestChunkIntraBatchDuplicate (BACKLOG R-24 §4.5/§4.6): two rows with the
// same hash inside one call produce the same DeterministicChunkID — the
// intra-batch filter must drop them, and no 25P02 may surface.
func TestChunkIntraBatchDuplicate(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: hash, Text: "body", TokenCount: 2},
			{Seq: 1, ChunkHash: hash, Text: "body", TokenCount: 2},
		}); err != nil {
			return err
		}
		return db.InsertParents(ctx, tx, doc, "books", []ParentChunk{
			{Seq: 0, ChunkHash: hash + "-p", Text: "parent", TokenCount: 2},
			{Seq: 1, ChunkHash: hash + "-p", Text: "parent", TokenCount: 2},
		})
	})
	if err != nil {
		t.Fatalf("intra-batch duplicate must be filtered, not aborted: %v", err)
	}
	n, err := db.CountDocChunks(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	// 2 children + 2 parents → 4 rows total
	if n != 4 {
		t.Fatalf("expected 4 chunk rows (2 child + 2 parent), got %d", n)
	}
}

// TestChunkRedeliveryMidBatch (BACKLOG R-24 §4.7): a batch containing an
// already-stored chunk plus fresh ones. COPY raises the unique violation,
// the savepoint rollback must leave the tx usable, and the fresh rows must
// land via the row-by-row fallback.
func TestChunkRedeliveryMidBatch(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	oldHash := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	freshHash := "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: oldHash, Text: "old", TokenCount: 1},
		}); err != nil {
			return err
		}
		// second call: COPY hits the pre-existing row mid-batch
		return db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: oldHash, Text: "old", TokenCount: 1},
			{Seq: 1, ChunkHash: freshHash, Text: "fresh", TokenCount: 1},
		})
	})
	if err != nil {
		t.Fatalf("mid-batch conflict must not abort the tx: %v", err)
	}
	n, err := db.CountDocChunks(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("fresh row lost after conflict rollback: %d chunks", n)
	}
}

// TestChunkFallbackTxStillUsable (BACKLOG R-24 §4.9): after a forced COPY
// failure the tx must survive a further Exec in the same transaction.
func TestChunkFallbackTxStillUsable(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	hash := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: hash, Text: "dup", TokenCount: 1},
		}); err != nil {
			return err
		}
		// forces the savepoint path inside InsertChunks
		if err := db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: hash, Text: "dup", TokenCount: 1},
		}); err != nil {
			return err
		}
		// same tx must still execute statements
		_, err := tx.Exec(ctx, `SELECT 1`)
		return err
	})
	if err != nil {
		t.Fatalf("tx must stay usable after COPY conflict: %v", err)
	}
}

// TestChunkCrossSetCollision (BACKLOG R-24 §4.10): parent + child with the
// identical hash in one tx → the parent row wins (inserted first), no child
// row, no 25P02. ON CONFLICT (doc_id, chunk_hash) ignores is_parent, so the
// child cannot coexist with the parent.
func TestChunkCrossSetCollision(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	doc := seedDoc(t, db)
	hash := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	err := db.Tx(ctx, func(tx txType) error {
		if err := db.InsertParents(ctx, tx, doc, "books", []ParentChunk{
			{Seq: 0, ChunkHash: hash, Text: "same text", TokenCount: 2},
		}); err != nil {
			return err
		}
		return db.InsertChunks(ctx, tx, doc, "books", []ChildChunk{
			{Seq: 0, ChunkHash: hash, Text: "same text", TokenCount: 2},
		})
	})
	if err != nil {
		t.Fatalf("cross-set collision must not abort the tx: %v", err)
	}
	n, err := db.CountDocChunks(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("parent must win cross-set collision: %d rows", n)
	}
	var isParent bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT is_parent FROM chunks WHERE doc_id = $1`, doc).Scan(&isParent); err != nil {
		t.Fatal(err)
	}
	if !isParent {
		t.Fatal("surviving row must be the parent")
	}
}

func TestKeyAuthValidityRules(t *testing.T) {
	db := mustDB(t, "paradedb/paradedb:17")
	ctx := context.Background()
	exp := time.Now().Add(-time.Hour) // expired
	_, err := db.CreateKey(ctx, "expired", "hash-expired", []string{"search"}, nil, &exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthenticateKey(ctx, "expired-raw"); err == nil {
		t.Fatal("unknown key must 401")
	}

	raw := "ragk_testkey"
	_, err = db.CreateKey(ctx, "live", HashKey(raw), []string{"search"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	k, err := db.AuthenticateKey(ctx, raw)
	if err != nil {
		t.Fatalf("live key rejected: %v", err)
	}
	if err := RequireScope(k, "search"); err != nil {
		t.Fatal(err)
	}
	if err := RequireScope(k, "admin"); err == nil {
		t.Fatal("missing scope must 403")
	}
	// revoked → invalid
	if _, err := db.RevokeKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthenticateKey(ctx, raw); err != ErrKeyInvalid {
		t.Fatalf("revoked key must be invalid, got %v", err)
	}
}

func seedDoc(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()
	m := &EmbeddingModel{
		Name: "seed-test-model", Provider: "tei", ModelID: "BAAI/bge-m3",
		IngestURL: "http://127.0.0.1:8081", QueryURL: "http://127.0.0.1:8082",
		VectorDim: 1024, QueryPrefix: "search_query: ",
		BatchSize: 48, CtxBudget: 1900, TruncateChars: 6000,
	}
	inserted, err := db.InsertEmbeddingModel(ctx, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertCollection(ctx, "books", "Books", inserted); err != nil {
		// idempotent on the testcontainers lane
		if _, gerr := db.GetCollection(ctx, "books"); gerr != nil {
			t.Fatal(err)
		}
	}
	id := fmt.Sprintf("11111111-1111-1111-1111-%012d", time.Now().UnixNano()%1e12)
	doc := &Document{
		ID:            id,
		CollectionID:  strPtr("books"),
		SourceURI:     "s3://raw/" + id + ".pdf",
		ContentSHA256: hashOf(id),
		State:         StateUploaded,
		Metadata:      map[string]any{},
	}
	if err := db.InsertDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	return id
}

func strPtr(s string) *string { return &s }

func hashOf(s string) string {
	return fmt.Sprintf("%064x", len(s))[:64]
}

type txType = txReal

var _ = testcontainers.GenericContainer
