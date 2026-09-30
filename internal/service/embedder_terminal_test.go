package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	// pgx/v5/stdlib for its init(): registers the "pgx" database/sql driver
	// used by wait.ForSQL below.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/saufi-opi/lybrix/internal/config"
	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Boot helper for the terminal-state integration lane: same image pin and
// skip/fatal discipline as internal/store's mustDB (R-40) — unreachable
// docker endpoint skips (dockerless dev), a boot error fatals (never a
// silent skip).
const termTestDBImage = "paradedb/paradedb:0.25.9-pg17"

func mustTermDB(t *testing.T) *store.DB {
	t.Helper()
	if !dockerReachable() {
		network, addr := dockerAddr()
		t.Skipf("docker endpoint unreachable: %s://%s", network, addr)
	}
	ctx := context.Background()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        termTestDBImage,
			Env:          map[string]string{"POSTGRES_USER": "rag", "POSTGRES_PASSWORD": "rag", "POSTGRES_DB": "rag"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port network.Port) string {
				return fmt.Sprintf("postgres://rag:rag@%s:%s/rag?sslmode=disable", host, port.Port())
			}),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("testcontainers boot: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })
	host, _ := ctr.Host(ctx)
	port, _ := ctr.MappedPort(ctx, "5432/tcp")
	dsn := fmt.Sprintf("postgres://rag:rag@%s:%s/rag?sslmode=disable", host, port.Port())
	db, err := store.NewPool(ctx, dsn, store.Timeouts{})
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(db.Close)
	return db
}

func dockerReachable() bool {
	network, addr := dockerAddr()
	conn, err := net.DialTimeout(network, addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func dockerAddr() (network, addr string) {
	host := os.Getenv("DOCKER_HOST")
	switch {
	case strings.HasPrefix(host, "unix://"):
		return "unix", strings.TrimPrefix(host, "unix://")
	case strings.HasPrefix(host, "tcp://"):
		return "tcp", strings.TrimPrefix(host, "tcp://")
	case host != "":
		return "tcp", host
	}
	return "unix", "/var/run/docker.sock"
}

// seedTerminalDoc inserts a doc row in the given state with the given
// settled-shard count, returning its id.
func seedTerminalDoc(t *testing.T, db *store.DB, state store.DocState, shardsDone, total int) string {
	t.Helper()
	ctx := context.Background()
	m := &store.EmbeddingModel{
		Name: fmt.Sprintf("term-test-model-%d", time.Now().UnixNano()%1e9), Provider: "tei",
		ModelID: "BAAI/bge-m3", IngestURL: "http://127.0.0.1:8081", QueryURL: "http://127.0.0.1:8082",
		VectorDim: 1024, QueryPrefix: "search_query: ",
		BatchSize: 48, CtxBudget: 1900, TruncateChars: 6000,
	}
	inserted, err := db.InsertEmbeddingModel(ctx, m, nil)
	if err != nil {
		t.Fatalf("model seed: %v", err)
	}
	if _, err := db.InsertCollection(ctx, "termbooks", "TermBooks", inserted); err != nil {
		if _, gerr := db.GetCollection(ctx, "termbooks"); gerr != nil {
			t.Fatalf("collection seed: %v", err)
		}
	}
	id := fmt.Sprintf("22222222-2222-2222-2222-%012d", time.Now().UnixNano()%1e12)
	doc := &store.Document{
		ID:            id,
		CollectionID:  strPtr("termbooks"),
		SourceURI:     "s3://raw/" + id + ".pdf",
		ContentSHA256: sha256Hex(id),
		State:         state,
		Metadata:      map[string]any{},
	}
	if err := db.InsertDocument(ctx, doc); err != nil {
		t.Fatal(err)
	}
	if total > 0 {
		if _, err := db.Pool.Exec(ctx,
			`UPDATE documents SET total_shards = $2, shards_done = $3 WHERE id = $1`,
			id, total, shardsDone); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// TestHandleEmbedTerminal (BACKLOG R-39): the poison-doc loop ends.
//
//	case A — settled doc with zero done shards: HandleEmbed marks FAILED
//	         (PDF_CORRUPT) and returns nil so the runner ACKs.
//	case B — nonexistent doc id: nil, no crash, no event row (FK risk).
//	case C — terminal (failed) doc: nil, state untouched.
//
// Redis/S3 are untouched on these paths; zero-value Settings suffices (the
// terminal paths read no settings).
func TestHandleEmbedTerminal(t *testing.T) {
	db := mustTermDB(t)
	ctx := context.Background()
	deps := Deps{Settings: &config.Settings{}, DB: db}

	t.Run("A: settled book with no shards fails terminally", func(t *testing.T) {
		docID := seedTerminalDoc(t, db, store.StateEmbedding, 0, 1)
		err := HandleEmbed(ctx, deps, nil, map[string]any{"doc_id": docID})
		if err != nil {
			t.Fatalf("HandleEmbed must return nil (ACK), got %v", err)
		}
		var state, code string
		if err := db.Pool.QueryRow(ctx,
			`SELECT state, error_code FROM documents WHERE id = $1`, docID).Scan(&state, &code); err != nil {
			t.Fatal(err)
		}
		if state != string(store.StateFailed) {
			t.Fatalf("doc must be failed, got %q", state)
		}
		if code != string(errors.CodePDFCorrupt) {
			t.Fatalf("error_code must be PDF_CORRUPT, got %q", code)
		}
	})

	t.Run("B: vanished doc row acks without crash", func(t *testing.T) {
		err := HandleEmbed(ctx, deps, nil, map[string]any{"doc_id": "99999999-9999-9999-9999-999999999999"})
		if err != nil {
			t.Fatalf("vanished doc row must ACK nil, got %v", err)
		}
	})

	t.Run("C: terminal doc acks no-op, state untouched", func(t *testing.T) {
		docID := seedTerminalDoc(t, db, store.StateFailed, 0, 0)
		err := HandleEmbed(ctx, deps, nil, map[string]any{"doc_id": docID})
		if err != nil {
			t.Fatalf("terminal doc must ACK nil, got %v", err)
		}
		var state string
		if err := db.Pool.QueryRow(ctx,
			`SELECT state FROM documents WHERE id = $1`, docID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != string(store.StateFailed) {
			t.Fatalf("terminal doc state must be untouched, got %q", state)
		}
	})
}

// sha256Hex is a stable hex encoding for the content-sha column — a plain
// sha256 over the doc id so seeds never collide on uq_doc_content.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
