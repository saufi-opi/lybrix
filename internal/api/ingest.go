package api

// Multi-source ingestion (PLAN.md §4 items 18-19): disk-streaming shared
// ingest helper + three REST paths.
//
//   - POST /v1/documents/fetch-url  — server-side streamed fetch → MinIO →
//     enqueue, with dial-time SSRF enforcement (every resolved IP validated
//     before connect, plus a Control hook re-checking the connected address
//     — defeats DNS rebinding; one shared client covers the whole redirect
//     chain so every hop is dial-checked too).
//   - POST /v1/connectors/opds/browse — OPDS/Atom catalog read (Basic
//     Auth, credentials never persisted).
//   - POST /v1/connectors/opds/sync — per-book acquisition → ingest.
//
// Semantics mirror handleCommit: backlog 429 gate, FindDuplicate 409,
// InsertDocument, one SplitJob, 202 {"id","state"}.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// ingest size policy (PLAN.md item 18):
//   - PDF: rough 40KB/page upper bound × MaxDocumentPages — the early stop
//     fires DURING the download, before the write completes.
//   - EPUB: hard ceiling 64 MiB, enforced by byte accounting mid-stream.
const epubMaxBytes = 64 * 1024 * 1024

// ingestResult is the typed outcome of ingestFromStream.
type ingestResult struct {
	DocID      string
	HTTPStatus int
	Detail     string
}

// ingestFromStream streams r to a temp file (disk, never RAM-buffered),
// digests sha256 along the way, sniffs PDF vs EPUB magic bytes, enforces
// the per-format size caps during the download, then puts the temp file
// into MinIO and inserts the doc + split job with handleCommit's
// semantics. The temp file is removed on all paths.
func (s *Server) ingestFromStream(ctx context.Context, r io.Reader, collectionID, filename, mimeType string, title, author *string, metadata map[string]any) ingestResult {
	// sniff magic from the first bytes before committing to a temp file
	head := make([]byte, 5)
	n, err := io.ReadFull(r, head)
	isEpub := false
	if err == nil && n == 5 {
		isEpub = string(head) == "PK\x03\x04"
	}
	if !isEpub && !(n >= 5 && string(head[:n]) == "%PDF-") {
		// allow a short-but-PDF-looking prefix? No — both formats need 5 bytes
		return ingestResult{HTTPStatus: http.StatusBadRequest, Detail: "unsupported file type"}
	}
	reader := io.MultiReader(bytesReader(head[:n]), r)

	var maxBytes int
	var mime, contentType string
	if isEpub {
		maxBytes = epubMaxBytes
		mime = "application/epub+zip"
		contentType = "application/epub+zip"
	} else {
		// ~40KB/page upper bound; page-count probe after the download
		maxBytes = s.deps.Settings.MaxDocumentPages * 40 * 1024
		mime = "application/pdf"
		contentType = "application/pdf"
	}

	tmpDir, err := os.MkdirTemp("", "ingest-")
	if err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "temp dir failed: " + err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	tmpFile, err := os.CreateTemp(tmpDir, "body-")
	if err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "temp file failed: " + err.Error()}
	}

	digest := sha256.New()
	tee := io.TeeReader(reader, digest)
	written, err := io.Copy(tmpFile, io.LimitReader(tee, int64(maxBytes)+1))
	closeErr := tmpFile.Close()
	if err == nil && written > int64(maxBytes) {
		return ingestResult{HTTPStatus: http.StatusBadRequest, Detail: "file too large"}
	}
	if err != nil {
		return ingestResult{HTTPStatus: http.StatusBadRequest, Detail: "download failed: " + err.Error()}
	}
	if closeErr != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "temp write failed: " + closeErr.Error()}
	}

	// Post-download verification, commit parity with verifyRawObject.
	if filename == "" {
		filename = "source." + map[bool]string{true: "epub", false: "pdf"}[isEpub]
	}
	if !isEpub {
		pageCount, perr := pipelinePageCount(tmpFile.Name())
		if perr != nil {
			return ingestResult{HTTPStatus: http.StatusBadRequest, Detail: "unreadable PDF: " + perr.Error()}
		}
		if pageCount > s.deps.Settings.MaxDocumentPages {
			return ingestResult{HTTPStatus: http.StatusBadRequest,
				Detail: strconv.Itoa(pageCount) + " pages over cap " + strconv.Itoa(s.deps.Settings.MaxDocumentPages)}
		}
		return s.ingestCommit(ctx, ingestCommitArgs{
			collectionID: collectionID, filename: filename, mimeType: mime,
			contentType: contentType, title: title, author: author,
			metadata: metadata, path: tmpFile.Name(),
			sha: hex.EncodeToString(digest.Sum(nil)), pageCount: &pageCount,
		})
	}
	one := 1
	return s.ingestCommit(ctx, ingestCommitArgs{
		collectionID: collectionID, filename: filename, mimeType: mime,
		contentType: contentType, title: title, author: author,
		metadata: metadata, path: tmpFile.Name(),
		sha: hex.EncodeToString(digest.Sum(nil)), pageCount: &one,
	})
}

// ingestCommitArgs carries the verified body into the commit-parity insert.
type ingestCommitArgs struct {
	collectionID  string
	filename      string
	mimeType      string
	contentType   string
	title, author *string
	metadata      map[string]any
	path          string // temp file to upload
	sha           string
	pageCount     *int
}

// ingestCommit is the commit-parity tail shared by every ingestion source:
// backlog 429 gate, FindDuplicate 409, InsertDocument, SplitJob, 202.
func (s *Server) ingestCommit(ctx context.Context, a ingestCommitArgs) ingestResult {
	// Backpressure: parse backlog over cap → 429 Retry-After (§6.1).
	backlog, err := queue.QueueDepth(ctx, s.deps.Redis, queue.StreamParse)
	if err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "queue depth read failed: " + err.Error()}
	}
	if backlog > int64(s.deps.Settings.MaxParseBacklog) {
		return ingestResult{HTTPStatus: http.StatusTooManyRequests,
			Detail: "parse backlog " + strconv.FormatInt(backlog, 10) + " over cap; retry later"}
	}
	if a.metadata == nil {
		a.metadata = map[string]any{}
	}
	docID := newUUID()
	if dup, err := s.deps.DB.FindDuplicate(ctx, a.collectionID, a.sha); err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: err.Error()}
	} else if dup != nil {
		return ingestResult{HTTPStatus: http.StatusConflict, Detail: "duplicate document in collection"}
	}
	if err := s.deps.S3.UploadFile(ctx, s.deps.Settings.S3BucketRaw, objectstore.RawKey(docID), a.contentType, a.path); err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "object upload failed: " + err.Error()}
	}
	doc := &store.Document{
		ID:            docID,
		CollectionID:  &a.collectionID,
		Title:         a.title,
		Author:        a.author,
		Filename:      &a.filename,
		SourceURI:     "s3://" + s.deps.Settings.S3BucketRaw + "/" + objectstore.RawKey(docID),
		ContentSHA256: a.sha,
		State:         store.StateUploaded,
		PageCount:     a.pageCount,
		MimeType:      &a.mimeType,
		Metadata:      a.metadata,
		UploadedBy:    keyName(KeyFromContext(ctx)),
	}
	if err := s.deps.DB.InsertDocument(ctx, doc); err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: err.Error()}
	}
	job := queue.SplitJob{
		SchemaVersion: queue.SchemaVersion,
		DocID:         docID,
		SourceURI:     doc.SourceURI,
	}
	if _, err := queue.XAddJob(ctx, s.deps.Redis, queue.StreamSplit, job); err != nil {
		return ingestResult{HTTPStatus: http.StatusInternalServerError, Detail: "enqueue split failed: " + err.Error()}
	}
	return ingestResult{DocID: docID, HTTPStatus: http.StatusAccepted, Detail: string(store.StateUploaded)}
}

// --- fetch-url -------------------------------------------------------------

// fetchURLRequest mirrors OpenAPI FetchUrlRequest.
type fetchURLRequest struct {
	CollectionID string         `json:"collection_id"`
	URL          string         `json:"url"`
	Title        *string        `json:"title"`
	Author       *string        `json:"author"`
	Metadata     map[string]any `json:"metadata"`
}

// ssrfGuardedClient builds the dial-time SSRF-enforcing http.Client: the
// transport's DialContext resolves the host and validates EVERY returned IP
// against private/loopback/link-local/multicast/reserved ranges before
// dialing, and the Control hook re-checks the actual connected address —
// the check runs at connect time on every connection, so DNS rebinding
// cannot slip a private address past a resolve-time check. One shared
// client serves the whole redirect chain: CheckRedirect keeps using the
// same transport, so every hop is dial-checked too.
func ssrfGuardedClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		// Control re-checks the actual connected address at connect time —
		// the DNS-rebinding backstop on top of the DialContext filter.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("ssrf guard: bad control addr %q", address)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("ssrf guard: non-IP connect address %q", host)
			}
			if isForbiddenIP(ip) {
				return fmt.Errorf("ssrf guard: connect to blocked address %s refused", ip)
			}
			return nil
		},
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, fmt.Errorf("ssrf guard: bad addr %q", addr)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("ssrf guard: resolve %s: %w", host, err)
			}
			var lastErr error
			for _, ip := range ips {
				if isForbiddenIP(ip.IP) {
					lastErr = fmt.Errorf("ssrf guard: %s resolves to a blocked address (%s)", host, ip.IP)
					continue
				}
				// dial this specific IP, pinning the host header via addr
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				if derr != nil {
					lastErr = derr
					continue
				}
				return conn, nil
			}
			if lastErr == nil {
				lastErr = fmt.Errorf("ssrf guard: %s resolved to no usable address", host)
			}
			return nil, lastErr
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			switch req.URL.Scheme {
			case "http", "https":
				// same client/transport → every hop dial-checked
				return nil
			default:
				return errors.New("ssrf guard: only http(s) redirects allowed")
			}
		},
	}
}

func isForbiddenIP(ip net.IP) bool {
	// loopback, RFC1918, link-local unicast/multicast, IPv6 unique-local,
	// link-local, IPv4 multicast + reserved, unspecified
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return true
	}
	// 169.254/16 covered by IsLinkLocalUnicast; add the IPv4 benchmark/
	// reserved ranges (100.64/10 CGNAT, 192.0.0/24, 192.0.2/24, 198.51.100/24,
	// 203.0.113/24, 240/4, 255.255.255.255) conservatively.
	if v4 := ip.To4(); v4 != nil {
		switch {
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127: // CGNAT 100.64/10
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 0: // 192.0.0/24
			return true
		case v4[0] == 192 && v4[1] == 0 && v4[2] == 2: // TEST-NET-1
			return true
		case v4[0] == 198 && v4[1] == 51 && v4[2] == 100: // TEST-NET-2
			return true
		case v4[0] == 203 && v4[1] == 0 && v4[2] == 113: // TEST-NET-3
			return true
		case v4[0] >= 240: // reserved + broadcast
			return true
		}
	}
	// IPv6 documentation 2001:db8::/32
	if len(ip) == 16 && ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x0d && ip[3] == 0xb8 {
		return true
	}
	return false
}

func (s *Server) handleFetchURL(w http.ResponseWriter, r *http.Request) {
	var body fetchURLRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.CollectionID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: collection_id")
		return
	}
	u, err := url.Parse(body.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "url must be an absolute http(s) URL")
		return
	}
	if col, err := s.deps.DB.GetCollection(r.Context(), body.CollectionID); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	} else if col == nil {
		writeDetail(w, http.StatusNotFound, "collection not found")
		return
	}

	client := ssrfGuardedClient()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, body.URL, nil)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "bad url: "+err.Error())
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		// dial-time rejections (SSRF, unreachable) surface as 400 with the
		// guard's reason — commit parity, never a 500.
		writeDetail(w, http.StatusBadRequest, "fetch failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeDetail(w, http.StatusBadRequest,
			fmt.Sprintf("remote returned %d", resp.StatusCode))
		return
	}
	// filename from the URL path (or Content-Disposition is overkill for v1)
	name := filepath.Base(u.Path)
	if name == "" || name == "." || name == "/" {
		name = "downloaded.pdf"
	}
	res := s.ingestFromStream(r.Context(), resp.Body, body.CollectionID, name,
		"", body.Title, body.Author, body.Metadata)
	if res.HTTPStatus == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "60")
	}
	if res.HTTPStatus == http.StatusAccepted {
		// 202 Accepted with {"id", "state"} — the commit contract.
		WriteJSON(w, http.StatusAccepted, map[string]any{"id": res.DocID, "state": res.Detail})
		return
	}
	writeDetail(w, res.HTTPStatus, res.Detail)
}

// --- OPDS ------------------------------------------------------------------

// opdsRequest carries the per-session connector credentials — never
// persisted server-side, never logged.
type opdsRequest struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	FeedURL  string `json:"feed_url"`
}

// opdsEntry is one parsed OPDS/Atom catalog entry.
type opdsEntry struct {
	Title       string        `json:"title"`
	Authors     []string      `json:"authors"`
	Summary     string        `json:"summary,omitempty"`
	Acquisition []opdsAcqLink `json:"acquisition"`
}

type opdsAcqLink struct {
	Href     string `json:"href"`
	MimeType string `json:"mime_type"`
}

// opdsBrowseResult mirrors OpenAPI OpdsBrowseResult.
type opdsBrowseResult struct {
	Entries  []opdsEntry `json:"entries"`
	NextHref *string     `json:"next_href"`
}

// atomFeed is the OPDS 1.x Atom subset the browse parser reads.
type atomFeed struct {
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
	Type string `xml:"type,attr"`
}

type atomEntry struct {
	Title   string       `xml:"title"`
	Summary string       `xml:"summary"`
	ID      string       `xml:"id"`
	Authors []atomAuthor `xml:"author"`
	Links   []atomLink   `xml:"link"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

// atomAcquisitionLinks extracts one entry's acquisition links (the OPDS
// rel or a direct pdf/epub mime type).
func atomAcquisitionLinks(e atomEntry) []opdsAcqLink {
	var out []opdsAcqLink
	for _, l := range e.Links {
		if l.Rel == "http://opds-spec.org/acquisition" ||
			strings.HasPrefix(l.Rel, "http://opds-spec.org/acquisition/") ||
			l.Type == "application/pdf" || l.Type == "application/epub+zip" {
			out = append(out, opdsAcqLink{Href: l.Href, MimeType: l.Type})
		}
	}
	return out
}

func opdsAuthedGet(ctx context.Context, client *http.Client, rawURL, username, password string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	return client.Do(req)
}

func (s *Server) handleOpdsBrowse(w http.ResponseWriter, r *http.Request) {
	var body opdsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	target := body.FeedURL
	if target == "" {
		target = body.URL
	}
	if u, err := url.Parse(target); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "url must be an absolute http(s) URL")
		return
	}
	resp, err := opdsAuthedGet(r.Context(), ssrfGuardedClient(), target, body.Username, body.Password)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "OPDS fetch failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		writeDetail(w, http.StatusBadRequest, "OPDS feed rejected credentials")
		return
	}
	if resp.StatusCode != http.StatusOK {
		writeDetail(w, http.StatusBadRequest, fmt.Sprintf("OPDS feed returned %d", resp.StatusCode))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "OPDS read failed: "+err.Error())
		return
	}
	var feed atomFeed
	if err := xml.Unmarshal(raw, &feed); err != nil || len(feed.Entries) == 0 {
		writeDetail(w, http.StatusBadRequest, "not an OPDS/Atom feed")
		return
	}
	out := opdsBrowseResult{Entries: make([]opdsEntry, 0, len(feed.Entries))}
	for _, e := range feed.Entries {
		entry := opdsEntry{Title: e.Title, Summary: e.Summary}
		for _, a := range e.Authors {
			if a.Name != "" {
				entry.Authors = append(entry.Authors, a.Name)
			}
		}
		entry.Acquisition = atomAcquisitionLinks(e)
		out.Entries = append(out.Entries, entry)
	}
	for _, l := range feed.Links {
		if l.Rel == "next" {
			out.NextHref = &l.Href
		}
	}
	WriteJSON(w, http.StatusOK, out)
}

// opdsSyncRequest mirrors OpenAPI OpdsSyncRequest.
type opdsSyncRequest struct {
	URL          string         `json:"url"`
	Username     string         `json:"username"`
	Password     string         `json:"password"`
	CollectionID string         `json:"collection_id"`
	Selection    []opdsEntryRef `json:"selection"`
}

// opdsEntryRef names one book to sync (from the browse result).
type opdsEntryRef struct {
	Title    string `json:"title"`
	Href     string `json:"href"`
	MimeType string `json:"mime_type"`
}

// opdsSyncResult mirrors OpenAPI OpdsSyncResult.
type opdsSyncResult struct {
	Synced  int              `json:"synced"`
	Failed  int              `json:"failed"`
	Results []opdsItemResult `json:"results"`
}

type opdsItemResult struct {
	Title  string  `json:"title"`
	Status string  `json:"status"` // accepted | duplicate | rejected
	DocID  *string `json:"doc_id,omitempty"`
	Detail *string `json:"detail,omitempty"`
}

func (s *Server) handleOpdsSync(w http.ResponseWriter, r *http.Request) {
	var body opdsSyncRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeDetail(w, http.StatusUnprocessableEntity, "invalid JSON body")
		return
	}
	if body.CollectionID == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "field required: collection_id")
		return
	}
	if u, err := url.Parse(body.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeDetail(w, http.StatusUnprocessableEntity, "url must be an absolute http(s) URL")
		return
	}
	if col, err := s.deps.DB.GetCollection(r.Context(), body.CollectionID); err != nil {
		writeDetail(w, http.StatusInternalServerError, err.Error())
		return
	} else if col == nil {
		writeDetail(w, http.StatusNotFound, "collection not found")
		return
	}

	// selection omitted → every acquisition entry on the given feed page.
	refs := body.Selection
	if len(refs) == 0 {
		browseBody := opdsRequest{URL: body.URL, Username: body.Username,
			Password: body.Password, FeedURL: body.URL}
		feed, status, detail := s.opdsFetchFeed(r.Context(), browseBody)
		if detail != "" {
			writeDetail(w, status, detail)
			return
		}
		for _, e := range feed.Entries {
			acqs := atomAcquisitionLinks(e)
			for _, acq := range acqs {
				if acq.MimeType == "application/pdf" || acq.MimeType == "application/epub+zip" {
					refs = append(refs, opdsEntryRef{Title: e.Title, Href: acq.Href, MimeType: acq.MimeType})
					break // one book = one best acquisition link
				}
			}
		}
	}

	client := ssrfGuardedClient()
	out := opdsSyncResult{Results: []opdsItemResult{}}
	for _, ref := range refs {
		item := opdsItemResult{Title: ref.Title}
		// per-item context timeout keeps one hung book from stalling the page
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		resp, err := opdsAuthedGet(ctx, client, ref.Href, body.Username, body.Password)
		if err != nil {
			item.Status = "rejected"
			d := "fetch failed: " + err.Error()
			item.Detail = &d
			out.Failed++
			out.Results = append(out.Results, item)
			cancel()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			item.Status = "rejected"
			d := fmt.Sprintf("remote returned %d", resp.StatusCode)
			item.Detail = &d
			out.Failed++
			out.Results = append(out.Results, item)
			cancel()
			continue
		}
		res := s.ingestFromStream(ctx, resp.Body, body.CollectionID, filepath.Base(ref.Href),
			ref.MimeType, &ref.Title, nil, nil)
		resp.Body.Close()
		cancel()
		switch res.HTTPStatus {
		case http.StatusAccepted:
			item.Status = "accepted"
			item.DocID = &res.DocID
			out.Synced++
		case http.StatusConflict:
			item.Status = "duplicate"
			item.Detail = &res.Detail
		default:
			item.Status = "rejected"
			item.Detail = &res.Detail
			out.Failed++
		}
		out.Results = append(out.Results, item)
	}
	WriteJSON(w, http.StatusOK, out)
}

// opdsFetchFeed fetches + parses one feed page (shared by sync's
// selection-omitted path).
func (s *Server) opdsFetchFeed(ctx context.Context, body opdsRequest) (*atomFeed, int, string) {
	target := body.FeedURL
	if target == "" {
		target = body.URL
	}
	resp, err := opdsAuthedGet(ctx, ssrfGuardedClient(), target, body.Username, body.Password)
	if err != nil {
		return nil, http.StatusBadRequest, "OPDS fetch failed: " + err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, http.StatusBadRequest, "OPDS feed rejected credentials"
	}
	if resp.StatusCode != http.StatusOK {
		return nil, http.StatusBadRequest, fmt.Sprintf("OPDS feed returned %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8*1024*1024))
	if err != nil {
		return nil, http.StatusBadRequest, "OPDS read failed: " + err.Error()
	}
	var feed atomFeed
	if err := xml.Unmarshal(raw, &feed); err != nil || len(feed.Entries) == 0 {
		return nil, http.StatusBadRequest, "not an OPDS/Atom feed"
	}
	return &feed, 0, ""
}

// bytesReader is a tiny io.Reader over a byte slice (ingest sniff re-read).
func bytesReader(b []byte) io.Reader { return strings.NewReader(string(b)) }

// pipelinePageCount delegates to the pipeline package's pdfcpu path-based
// wrapper (pdfcpu takes a path, so no full-buffer needed).
func pipelinePageCount(path string) (int, error) { return pipeline.PageCount(path) }
