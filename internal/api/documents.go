package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// newUUID returns a random RFC-4122 v4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// TxT is the transaction handle type the store works against.
type TxT = pgx.Tx

// presignRequest mirrors OpenAPI PresignRequest.
type presignRequest struct {
	CollectionID string `json:"collection_id"`
	ByteSize     int64  `json:"byte_size"`
}

// presignResponse mirrors OpenAPI PresignResponse.
type presignResponse struct {
	DocID     string `json:"doc_id"`
	UploadURL string `json:"upload_url"`
}

// commitRequest mirrors OpenAPI CommitRequest.
type commitRequest struct {
	CollectionID  string         `json:"collection_id"`
	ContentSHA256 string         `json:"content_sha256"`
	Title         *string        `json:"title"`
	Author        *string        `json:"author"`
	Metadata      map[string]any `json:"metadata"`
}

// documentOut mirrors OpenAPI DocumentOut field-for-field.
type documentOut struct {
	ID           string    `json:"id"`
	CollectionID *string   `json:"collection_id"`
	Title        *string   `json:"title"`
	Author       *string   `json:"author"`
	PageCount    *int      `json:"page_count"`
	ByteSize     *int64    `json:"byte_size"`
	State        string    `json:"state"`
	TotalShards  *int      `json:"total_shards"`
	ShardsDone   int       `json:"shards_done"`
	ShardsFailed int       `json:"shards_failed"`
	Completeness *float64  `json:"completeness"`
	ErrorCode    *string   `json:"error_code"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toDocumentOut(d *store.Document) documentOut {
	return documentOut{
		ID:           d.ID,
		CollectionID: d.CollectionID,
		Title:        d.Title,
		Author:       d.Author,
		PageCount:    d.PageCount,
		ByteSize:     d.ByteSize,
		State:        string(d.State),
		TotalShards:  d.TotalShards,
		ShardsDone:   d.ShardsDone,
		ShardsFailed: d.ShardsFailed,
		Completeness: d.Completeness,
		ErrorCode:    d.ErrorCode,
		CreatedAt:    d.CreatedAt,
		UpdatedAt:    d.UpdatedAt,
	}
}

// shardOut mirrors OpenAPI ShardOut.
type shardOut struct {
	Idx        int     `json:"idx"`
	PageStart  int     `json:"page_start"`
	PageEnd    int     `json:"page_end"`
	State      string  `json:"state"`
	Attempts   int     `json:"attempts"`
	NeedsOCR   bool    `json:"needs_ocr"`
	DurationMS *int    `json:"duration_ms"`
	PeakRSSMB  *int    `json:"peak_rss_mb"`
	ErrorCode  *string `json:"error_code"`
}

// retryRequest mirrors OpenAPI RetryRequest.
type retryRequest struct {
	Scope string `json:"scope"`
}

func (s *Server) handlePresign(w http.ResponseWriter, r *http.Request) {
	var body presignRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	docID := newUUID()
	url, err := s.deps.S3.PresignPut(r.Context(), s.deps.Settings.S3BucketRaw,
		objectstore.RawKey(docID), time.Hour)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "presign failed: "+err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, presignResponse{DocID: docID, UploadURL: url})
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "doc_id")
	if docID == "" || !isUUID(docID) {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid doc_id")
		return
	}
	var body commitRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.CollectionID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: collection_id")
		return
	}
	if len(body.ContentSHA256) != 64 {
		writeDetail(w, http.StatusUnprocessableEntity,
			"content_sha256 must be exactly 64 characters")
		return
	}
	if body.Metadata == nil {
		body.Metadata = map[string]any{}
	}

	// Backpressure: if the parse backlog exceeds MAX_PARSE_BACKLOG, commit
	// returns 429 with Retry-After (§6.1).
	backlog, err := queue.QueueDepth(r.Context(), s.deps.Redis, queue.StreamParse)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, "queue depth read failed: "+err.Error())
		return
	}
	if backlog > int64(s.deps.Settings.MaxParseBacklog) {
		w.Header().Set("Retry-After", "60")
		writeDetail(w, http.StatusTooManyRequests,
			"parse backlog "+strconv.FormatInt(backlog, 10)+" over cap; retry later")
		return
	}

	if dup, err := s.deps.DB.FindDuplicate(r.Context(), body.CollectionID, body.ContentSHA256); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	} else if dup != nil {
		writeDetail(w, http.StatusConflict, "duplicate document in collection")
		return
	}

	// One streaming pass serves all three checks (R-15/R-21): the verified
	// hash below is client-supplied but only reaches the row when it equals
	// the server-computed value — otherwise the request 400s.
	rawKey := objectstore.RawKey(docID)
	pageCount, err := s.verifyRawObject(r.Context(), rawKey, body.ContentSHA256)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, err.Error())
		return
	}

	doc := &store.Document{
		ID:            docID,
		CollectionID:  &body.CollectionID,
		Title:         body.Title,
		Author:        body.Author,
		SourceURI:     "s3://" + s.deps.Settings.S3BucketRaw + "/" + rawKey,
		ContentSHA256: body.ContentSHA256,
		State:         store.StateUploaded,
		PageCount:     &pageCount,
		Metadata:      body.Metadata,
		UploadedBy:    keyName(KeyFromContext(r.Context())),
	}
	if err := s.deps.DB.InsertDocument(r.Context(), doc); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	job := queue.SplitJob{
		SchemaVersion: queue.SchemaVersion,
		DocID:         docID,
		SourceURI:     doc.SourceURI,
	}
	if _, err := queue.XAddJob(r.Context(), s.deps.Redis, queue.StreamSplit, job); err != nil {
		writeDetail(w, http.StatusInternalServerError, "enqueue split failed: "+err.Error())
		return
	}
	// 202 Accepted with {"id", "state"} — the commit contract.
	WriteJSON(w, http.StatusAccepted, map[string]any{"id": docID, "state": string(store.StateUploaded)})
}

// verifyRawObject streams the raw object once: sha256 + format sniff +
// per-format verification (R-15/R-21). PRD §6.1: the API — not the client —
// owns the dedupe key; §11: reject non-ingestible bodies and over-cap books
// at the door rather than inside a parser. The page-count probe only runs
// for PDF (the cap is a page cap); every other format verifies as one
// synthetic page.
func (s *Server) verifyRawObject(ctx context.Context, rawKey, clientSHA string) (int, error) {
	body, err := s.deps.S3.GetReader(ctx, s.deps.Settings.S3BucketRaw, rawKey)
	if err != nil {
		// a skipped PUT reads as a client error, not a 500
		return 0, errObjectNotUploaded
	}
	defer body.Close()

	digest := sha256.New()
	buf := make([]byte, 64*1024)
	// first read decides the format (the shared sniff table — Workstream 2)
	n, err := io.ReadFull(body, buf[:64])
	if err != nil && err != io.ErrUnexpectedEOF {
		return 0, errObjectNotUploaded
	}
	head := buf[:n]
	digest.Write(head)
	format, ferr := pipeline.DetectFormat(head, rawKey)
	if ferr != nil {
		return 0, errObjectNotPDF
	}
	if format != pipeline.FmtPDF {
		// non-PDF: stream the rest through the digest, then apply the
		// same verification table as ingestFromStream (zip integrity /
		// UTF-8 text / html markup). No page-count probe — pageCount = 1.
		nonPdf := head
		for {
			n, err := body.Read(buf)
			if n > 0 {
				digest.Write(buf[:n])
				nonPdf = append(nonPdf, buf[:n]...)
				// bound memory: the 64 MiB ceiling mirrors ingestFromStream
				if len(nonPdf) > nonPdfMaxBytes {
					return 0, errTooLarge
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return 0, errObjectNotUploaded
			}
		}
		if hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(clientSHA) {
			return 0, errSHAMismatch
		}
		// verify against a temp file (zip.OpenReader takes a path)
		tmp, terr := os.CreateTemp("", "verify-")
		if terr != nil {
			return 0, terr
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		if _, werr := tmp.Write(nonPdf); werr != nil {
			tmp.Close()
			return 0, werr
		}
		if cerr := tmp.Close(); cerr != nil {
			return 0, cerr
		}
		if verr := verifyNonPDF(format, tmpPath); verr != nil {
			return 0, errUnreadablePDF{msg: verr.Error()}
		}
		return 1, nil
	}
	pdfBytes := head
	for {
		n, err := body.Read(buf)
		if n > 0 {
			digest.Write(buf[:n])
			pdfBytes = append(pdfBytes, buf[:n]...)
			// bound memory: page-count probe needs the whole PDF for pdfcpu,
			// but an over-cap book would buffer forever — stop early past
			// the cap threshold using a rough 40KB/page upper bound.
			if len(pdfBytes) > s.deps.Settings.MaxDocumentPages*40*1024 {
				return 0, errTooLarge
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errObjectNotUploaded
		}
	}
	if hex.EncodeToString(digest.Sum(nil)) != strings.ToLower(clientSHA) {
		return 0, errSHAMismatch
	}
	pageCount, err := pdfPageCount(pdfBytes)
	if err != nil {
		return 0, errUnreadablePDF{msg: err.Error()}
	}
	if pageCount > s.deps.Settings.MaxDocumentPages {
		return 0, errOverCap{pages: pageCount, cap: s.deps.Settings.MaxDocumentPages}
	}
	return pageCount, nil
}

// typed verify errors map to distinct 400 details (documents.py parity).
var (
	errObjectNotUploaded = errors.New("object not uploaded")
	errObjectNotPDF      = errors.New("uploaded object is not an ingestible file")
	errSHAMismatch       = errors.New("content_sha256 mismatch")
	errTooLarge          = errors.New("uploaded object too large to verify")
)

type errUnreadablePDF struct{ msg string }

func (e errUnreadablePDF) Error() string { return "unreadable PDF: " + e.msg }

type errOverCap struct{ pages, cap int }

func (e errOverCap) Error() string {
	return strconv.Itoa(e.pages) + " pages over cap " + strconv.Itoa(e.cap)
}

func keyName(k *store.ApiKey) *string {
	if k == nil || k.Name == nil {
		return nil
	}
	return k.Name
}

func (s *Server) handleListDocuments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var state *store.DocState
	if v := q.Get("state"); v != "" {
		st := store.DocState(v)
		state = &st
	}
	var collection *string
	if v := q.Get("collection"); v != "" {
		collection = &v
	}
	var search *string
	if v := q.Get("q"); v != "" {
		search = &v
	}
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	docs, err := s.deps.DB.ListDocuments(r.Context(), state, collection, search, limit, offset)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]documentOut, 0, len(docs))
	for _, d := range docs {
		out = append(out, toDocumentOut(d))
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	doc, err := s.deps.DB.GetDocument(r.Context(), chi.URLParam(r, "doc_id"))
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc == nil {
		writeDetail(w, http.StatusNotFound, "document not found")
		return
	}
	WriteJSON(w, http.StatusOK, toDocumentOut(doc))
}

// handleDeleteDocument: single-doc delete — DB row goes, chunks/shards
// cascade (store.DeleteDocument); the MinIO object is left for janitor GC
// with an events row noting the orphan (§ Phase 1: object left, log event).
// Scope: admin (routeScope). Key collection scope: the doc's collection must
// fall within the key's allowlist (keyScopeChunks pattern).
func (s *Server) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	docID := chi.URLParam(r, "doc_id")
	if docID == "" || !isUUID(docID) {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid doc_id")
		return
	}
	doc, err := s.deps.DB.GetDocument(ctx, docID)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc == nil {
		writeDetail(w, http.StatusNotFound, "document not found")
		return
	}
	if !keyAllowedCollection(w, KeyFromContext(ctx), doc.CollectionID) {
		return
	}
	if err := s.deps.DB.DeleteDocument(ctx, docID); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	title := "(untitled)"
	if doc.Title != nil {
		title = *doc.Title
	}
	_ = s.deps.DB.WriteEventPool(ctx, "info", "documents",
		"document deleted: "+title+" (raw object left for janitor GC)", store.EventDocID(docID), nil, nil, nil, nil)
	WriteJSON(w, http.StatusOK, map[string]any{"id": docID, "deleted": true})
}

// batchRequest mirrors OpenAPI DocumentBatchRequest — one body, two actions:
// delete removes every named doc (cascade), reparse requeues each doc's
// failed shards (handleRetry scope=shards logic per doc).
type batchRequest struct {
	Action string   `json:"action"`
	DocIDs []string `json:"doc_ids"`
}

// batchResultOut is the per-doc outcome list of the batch response.
type batchResultOut struct {
	DocID  string `json:"doc_id"`
	Status string `json:"status"` // ok | missing | error
	Detail string `json:"detail,omitempty"`
}

// handleDocumentBatch: fail-closed multi-collection guard — resolve EVERY
// doc_id's collection BEFORE mutating anything; an unknown id (404-able) is
// a 422 for the whole request, an out-of-scope collection is a 403 for the
// whole request. Never partially apply.
func (s *Server) handleDocumentBatch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body batchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	switch body.Action {
	case "delete", "reparse":
	default:
		writeDetail(w, http.StatusUnprocessableEntity, "action must be one of delete|reparse")
		return
	}
	if len(body.DocIDs) == 0 {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: doc_ids")
		return
	}
	// malformed ids read as unknown docs — same 422, one pass
	for _, id := range body.DocIDs {
		if !isUUID(id) {
			writeDetail(w, http.StatusUnprocessableEntity, "unknown doc_id: "+id)
			return
		}
	}

	// Resolve every doc up front (the fail-closed gate: one query per id is
	// fine at batch sizes the UI sends — tens, not thousands).
	docs := make([]*store.Document, 0, len(body.DocIDs))
	for _, id := range body.DocIDs {
		doc, err := s.deps.DB.GetDocument(ctx, id)
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		if doc == nil {
			writeDetail(w, http.StatusUnprocessableEntity, "unknown doc_id: "+id)
			return
		}
		docs = append(docs, doc)
	}
	// Whole-batch collection scope check: EVERY collection must fall within
	// the key's allowlist — stricter than the single-doc per-request guard.
	if key := KeyFromContext(ctx); key != nil && len(key.Collections) > 0 {
		for _, doc := range docs {
			if doc.CollectionID != nil && !store.CollectionAllowed(key, *doc.CollectionID) {
				writeDetail(w, http.StatusForbidden,
					"key is not scoped to collection "+*doc.CollectionID)
				return
			}
		}
	}

	out := make([]batchResultOut, 0, len(body.DocIDs))
	switch body.Action {
	case "delete":
		for _, doc := range docs {
			if err := s.deps.DB.DeleteDocument(ctx, doc.ID); err != nil {
				out = append(out, batchResultOut{DocID: doc.ID, Status: "error", Detail: err.Error()})
				continue
			}
			title := "(untitled)"
			if doc.Title != nil {
				title = *doc.Title
			}
			_ = s.deps.DB.WriteEventPool(ctx, "info", "documents",
				"document batch-deleted: "+title+" (raw object left for janitor GC)",
				store.EventDocID(doc.ID), nil, nil, nil, nil)
			out = append(out, batchResultOut{DocID: doc.ID, Status: "ok"})
		}
	case "reparse": // handleRetry scope=shards logic per doc
		for _, doc := range docs {
			status, detail := s.batchReparseDoc(ctx, doc)
			out = append(out, batchResultOut{DocID: doc.ID, Status: status, Detail: detail})
		}
	}
	WriteJSON(w, http.StatusOK, map[string]any{"action": body.Action, "results": out})
}

// batchReparseDoc requeues one doc's failed shards — the retry handler's
// scope=shards branch, lifted verbatim so batch and single-doc retry agree
// on state transitions and counter hygiene.
func (s *Server) batchReparseDoc(ctx context.Context, doc *store.Document) (string, string) {
	shards, err := s.deps.DB.FailedShards(ctx, doc.ID)
	if err != nil {
		return "error", err.Error()
	}
	if len(shards) == 0 {
		return "skipped", "no failed shards to retry"
	}
	err = s.deps.DB.Tx(ctx, func(tx pgx.Tx) error {
		// Same counter hygiene as handleRetry: shards_failed was bumped per
		// failure; requeueing undoes those failures. Back to PARSING so the
		// janitor's recovery sweeps see the book again.
		return s.deps.DB.SetDocState(ctx, tx, doc.ID, store.StateParsing, nil, nil)
	})
	if err != nil {
		return "error", err.Error()
	}
	for _, sh := range shards {
		if _, err := queue.XAddJob(ctx, s.deps.Redis, queue.StreamParse, queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         doc.ID,
			Idx:           sh.Idx,
			PageStart:     sh.PageStart,
			PageEnd:       sh.PageEnd,
			SourceURI:     doc.SourceURI,
		}); err != nil {
			return "error", err.Error()
		}
	}
	return "ok", ""
}

func (s *Server) handleGetShards(w http.ResponseWriter, r *http.Request) {
	shards, err := s.deps.DB.GetShards(r.Context(), chi.URLParam(r, "doc_id"))
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]shardOut, 0, len(shards))
	for _, sh := range shards {
		out = append(out, shardOut{
			Idx: sh.Idx, PageStart: sh.PageStart, PageEnd: sh.PageEnd,
			State: string(sh.State), Attempts: sh.Attempts, NeedsOCR: sh.NeedsOCR,
			DurationMS: sh.DurationMS, PeakRSSMB: sh.PeakRSSMB, ErrorCode: sh.ErrorCode,
		})
	}
	WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	docID := chi.URLParam(r, "doc_id")
	var body retryRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	switch body.Scope {
	case "shards", "embed", "full":
	default:
		writeDetail(w, http.StatusUnprocessableEntity,
			"scope must be one of shards|embed|full")
		return
	}
	doc, err := s.deps.DB.GetDocument(r.Context(), docID)
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	if doc == nil {
		writeDetail(w, http.StatusNotFound, "document not found")
		return
	}
	ctx := r.Context()
	switch body.Scope {
	case "embed":
		err = s.deps.DB.Tx(ctx, func(tx pgx.Tx) error {
			return s.deps.DB.SetDocState(ctx, tx, docID, store.StateEmbedding, nil, nil)
		})
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		_, err = queue.XAddJob(ctx, s.deps.Redis, queue.StreamEmbed, queue.EmbedJob{
			SchemaVersion: queue.SchemaVersion, DocID: docID,
		})
	case "full":
		err = s.deps.DB.Tx(ctx, func(tx pgx.Tx) error {
			return s.deps.DB.SetDocState(ctx, tx, docID, store.StateSplitting, nil, nil)
		})
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		_, err = queue.XAddJob(ctx, s.deps.Redis, queue.StreamSplit, queue.SplitJob{
			SchemaVersion: queue.SchemaVersion, DocID: docID, SourceURI: doc.SourceURI,
		})
	default: // shards: requeue failed shards only
		shards, err2 := s.deps.DB.FailedShards(ctx, docID)
		if err2 != nil {
			writeDetail(w, http.StatusInternalServerError, err2.Error())
			return
		}
		if len(shards) == 0 {
			writeDetail(w, http.StatusConflict, "no failed shards to retry")
			return
		}
		err = s.deps.DB.Tx(ctx, func(tx pgx.Tx) error {
			// Counter hygiene inside RequeueFailedShards: mark_shard_failed
			// bumped shards_failed per failure; requeueing undoes those
			// failures. Back to PARSING so the janitor's settled-book sweep
			// and stuck-doc warnings see this book again (a stranded pending
			// shard in a terminal-state doc is invisible to every recovery
			// path — R-11).
			return s.deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil)
		})
		if err != nil {
			writeDetail(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, sh := range shards {
			_, err = queue.XAddJob(ctx, s.deps.Redis, queue.StreamParse, queue.ParseJob{
				SchemaVersion: queue.SchemaVersion,
				DocID:         docID,
				Idx:           sh.Idx,
				PageStart:     sh.PageStart,
				PageEnd:       sh.PageEnd,
				SourceURI:     doc.SourceURI,
			})
			if err != nil {
				writeDetail(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"id": docID, "retried": body.Scope})
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !isHexDigit(byte(c)) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// pdfPageCount delegates to the pipeline package's pdfcpu wrapper.
func pdfPageCount(b []byte) (int, error) { return pipeline.PageCountBytes(b) }
