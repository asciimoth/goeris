// Package url retrieves HTTP and HTTPS sources under ERIS Fetch Protocol v1.
package url

import (
	"context"
	"errors"
	"io"
	"net/http"
	neturl "net/url"
	"strings"

	eris "github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/fetch"
	"github.com/asciimoth/goeris/fetch/internal/sourcehash"
	"github.com/asciimoth/goeris/internal/streamio"
)

// Config supplies the fetcher's dependencies. Callbacks run
// synchronously within each fetch. Dependencies must support concurrent use
// if fetches overlap. The client must enforce any caller-specific destination
// and credential policy, including checks on resolved network addresses.
// A transport, redirect policy or capability store can return
// fetch.Failure{Code: "denied"} to report a policy refusal.
// Verbose enables diagnostic messages that contain no source URLs or secrets.
type Config struct {
	Client *http.Client
	// Store writes downloaded blobs, files, and image indexes.
	Store    eris.CapabilityStore
	Progress Progress
	Log      func(string)
	Verbose  bool
	// Locks reuses capabilities from a trusted database. Nil disables locks.
	Locks fetch.LockDB
	// TempStorage holds each download before checksum verification and encoding.
	// Nil uses memory.
	TempStorage fetch.TempStorage
	// MaxBytes limits the size of each download. Exceeding it stops the fetch.
	// Zero disables the limit.
	MaxBytes int64
}

// Fetcher validates and retrieves URL descriptions.
type Fetcher struct{ config Config }

// New uses the supplied transport, cookie jar, timeout and redirect policy.
// The client configuration is captured at construction. Its timeout limits
// each HTTP retrieval. Use the fetch context to set a request-wide deadline.
func New(c Config) (*Fetcher, error) {
	if c.Client == nil || c.Store == nil || c.MaxBytes < 0 {
		return nil, errors.New("url fetcher requires a client, a capability store and a nonnegative byte limit")
	}
	client := c.Client
	redirect := client.CheckRedirect
	checkRedirect := func(req *http.Request, via []*http.Request) error {
		req.URL.Fragment = ""
		req.URL.RawFragment = ""
		// The automatically generated Referer can contain a private query.
		req.Header.Del("Referer")
		if len(via) > 10 || !fetch.ValidHTTPURI(req.URL.String()) || via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
			return errors.New("invalid redirect")
		}
		for _, prev := range via {
			if prev.URL.Scheme == req.URL.Scheme &&
				strings.EqualFold(prev.URL.Hostname(), req.URL.Hostname()) &&
				httpPort(prev.URL) == httpPort(req.URL) &&
				prev.URL.RequestURI() == req.URL.RequestURI() {
				return errors.New("redirect loop")
			}
		}
		if redirect != nil {
			return redirect(req, via)
		}
		return nil
	}
	c.Client = &http.Client{Transport: client.Transport, Jar: client.Jar, Timeout: client.Timeout, CheckRedirect: checkRedirect}
	return &Fetcher{config: c}, nil
}

func httpPort(u *neturl.URL) string {
	if p := u.Port(); p != "" {
		return strings.TrimLeft(p, "0")
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

func failure(code string) fetch.Response {
	return fetch.Response{Status: "fail", Failure: fetch.Failure{Code: code}}
}
func stopCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "limit"
	}
	return "cancelled"
}

// Fetch validates the source and checks the exact lock if configured. A request
// without a checksum can also use a minified lock for its primary URL or a
// mirror. On a miss Fetch tries each location in order. It attempts to bind the
// exact source after a minified lock hit or after successful retrieval and
// encoding. In the latter case it also attempts to bind the minified source for
// the URL that supplied the data.
func (f *Fetcher) Fetch(ctx context.Context, s fetch.Source) fetch.Response {
	if s.Type() != "url" {
		return fetch.Response{Status: "fail", Failure: fetch.Failure{Code: "unsupported-type", Key: "type"}}
	}
	opts, err := s.Validate(true)
	if err != nil {
		return fetch.Response{Status: "fail", Failure: err.(fetch.Failure)}
	}
	if err := ctx.Err(); err != nil {
		return failure(stopCode(err))
	}
	candidates := append(s.Values("url"), s.Values("alt")...)
	var checksum *fetch.Checksum
	if values := s.Values("checksum"); len(values) != 0 {
		// Validate checked the checksum grammar above.
		c, _ := fetch.ParseChecksum(values[0])
		checksum = &c
	}
	locks := f.config.Locks
	if locks == nil {
		result, _ := f.retrieve(ctx, s, opts, candidates, checksum)
		return result
	}

	exact := s.Hash()
	aliases := candidates
	if checksum != nil {
		aliases = nil
	}
	for hash := range lockHashes(exact, s, opts, aliases) {
		if err := ctx.Err(); err != nil {
			return failure(stopCode(err))
		}
		urn := locks.Lookup(ctx, hash)
		if err := ctx.Err(); err != nil {
			return failure(stopCode(err))
		}
		cap, err := eris.ParseURN(urn)
		if err != nil {
			continue
		}
		if hash != exact && ctx.Err() == nil {
			locks.Add(ctx, exact, urn)
		}
		return fetch.Response{Status: "ok", Kind: opts.Kind, Cap: cap}
	}
	result, selected := f.retrieve(ctx, s, opts, candidates, checksum)
	if result.Status != "ok" || ctx.Err() != nil {
		return result
	}
	urn := result.Cap.String()
	locks.Add(ctx, exact, urn)
	if ctx.Err() != nil {
		return result
	}
	alias := sourcehash.Sum256(minifiedURLPrefix(s, opts) + selected + "\n")
	if alias != exact && ctx.Err() == nil {
		locks.Add(ctx, alias, urn)
	}
	return result
}

func (f *Fetcher) retrieve(ctx context.Context, s fetch.Source, opts fetch.Options, candidates []string, checksum *fetch.Checksum) (fetch.Response, string) {
	var last string
	mismatch := false
	report := f.config.Progress
	root := ProgressNode{Name: "Checking URLs", Total: uint64(len(candidates))}
	var downloading func(uint64, int64)
	var completed func()
	if report != nil {
		downloading = func(size uint64, length int64) {
			state := downloadProgress(size, length)
			if len(candidates) == 1 {
				report(state)
			} else {
				snapshot := root
				snapshot.Child = &state
				report(snapshot)
			}
		}
		if len(candidates) > 1 {
			report(root)
			completed = func() {
				root.Completed++
				report(root)
			}
		}
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return failure(stopCode(err)), ""
		}
		if f.config.Verbose && f.config.Log != nil {
			f.config.Log("Fetching URL candidate")
		}
		result, retry := f.fetchCandidate(ctx, s, opts, candidate, checksum, downloading, completed)
		if err := ctx.Err(); err != nil {
			return failure(stopCode(err)), ""
		}
		if !retry {
			return result, candidate
		}
		last = result.Failure.Code
		mismatch = mismatch || last == "integrity"
	}
	if mismatch {
		return failure("integrity"), ""
	}
	return failure(last), ""
}

func (f *Fetcher) fetchCandidate(ctx context.Context, s fetch.Source, opts fetch.Options, uri string, checksum *fetch.Checksum, progress func(uint64, int64), completed func()) (result fetch.Response, retry bool) {
	body, code := f.download(ctx, uri, progress)
	if body != nil {
		defer func() {
			if body.Close() != nil {
				result, retry = failure("internal"), false
			}
		}()
		if checksum != nil && ctx.Err() == nil {
			if r, err := body.Reader(); err != nil {
				code = "internal"
			} else {
				// Only verification of a complete stream can report a mismatch.
				switch checksum.VerifyContext(ctx, r) {
				case nil:
				case fetch.Failure{Code: "integrity"}:
					code = "integrity"
				default:
					code = "internal"
				}
			}
		}
	}
	if completed != nil {
		completed()
	}
	if err := ctx.Err(); err != nil {
		return failure(stopCode(err)), false
	}
	if code != "" {
		// Only acquisition failures can advance to another mirror.
		return failure(code), code != "limit" && code != "internal"
	}
	r, err := body.Reader()
	if err != nil {
		return failure("internal"), false
	}
	cap, err := f.encode(ctx, s, opts, tempReader{ctx, r})
	switch {
	case errors.Is(err, errTempRead):
		return failure("internal"), false
	case errors.Is(err, fetch.Failure{Code: "denied"}):
		return failure("denied"), false
	case err != nil || cap.Validate() != nil:
		return failure("store"), false
	}
	return fetch.Response{Status: "ok", Kind: opts.Kind, Cap: cap}, false
}

var errTempRead = errors.New("read temporary data")

type tempReader struct {
	ctx context.Context
	r   io.Reader
}

func (r tempReader) Read(p []byte) (int, error) {
	n, err := streamio.ReadFull(r.ctx, r.r, p)
	if err != nil && err != io.EOF {
		err = errors.Join(errTempRead, err)
	}
	return n, err
}

func (f *Fetcher) encode(ctx context.Context, s fetch.Source, o fetch.Options, r io.Reader) (eris.Capability, error) {
	if o.Kind == "blob" {
		return f.config.Store.EncodeContentRecommended(ctx, r, o.Secret, eris.BlockOptions{})
	}
	tree, err := o.NewTree(f.config.Store, eris.BlockOptions{})
	if err != nil {
		return eris.Capability{}, err
	}
	executable := s.Values("executable")
	if err = tree.AddFile(ctx, s.Values("name")[0], r, len(executable) > 0 && executable[0] == "true"); err != nil {
		return eris.Capability{}, err
	}
	return tree.Finish(ctx)
}

func (f *Fetcher) download(ctx context.Context, uri string, progress func(uint64, int64)) (data fetch.TempData, code string) {
	if err := ctx.Err(); err != nil {
		return nil, stopCode(err)
	}
	if !strings.HasPrefix(uri, "http:") && !strings.HasPrefix(uri, "https:") {
		return nil, "unsupported-scheme"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "unavailable"
	}
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := f.config.Client.Do(req)
	if err != nil {
		if errors.Is(err, fetch.Failure{Code: "denied"}) {
			return nil, "denied"
		}
		return nil, "unavailable"
	}
	var body fetch.TempData
	defer func() {
		// Release the response before passing the completed data to the encoder.
		if resp.Body.Close() != nil && code == "" {
			data, code = nil, "unavailable"
		}
		if data == nil && body != nil && body.Close() != nil && code != "limit" {
			code = "internal"
		}
	}()
	switch resp.StatusCode {
	case 401, 403:
		return nil, "denied"
	case 404, 410:
		return nil, "not-found"
	case 200:
	default:
		return nil, "unavailable"
	}
	enc := resp.Header.Values("Content-Encoding")
	if resp.Uncompressed || resp.Header.Values("Content-Range") != nil || len(enc) > 0 && (len(enc) != 1 || !strings.EqualFold(enc[0], "identity")) {
		return nil, "unavailable"
	}
	if limit := f.config.MaxBytes; limit > 0 && resp.ContentLength > limit {
		return nil, "limit"
	}
	buffer, err := fetch.NewTempData(ctx, f.config.TempStorage)
	if err != nil {
		return nil, "internal"
	}
	body = buffer
	code = f.read(ctx, resp.Body, resp.ContentLength, body, progress)
	if code != "" {
		return nil, code
	}
	// The transport can populate Trailer only after the body reaches EOF.
	for _, field := range []string{"Content-Length", "Content-Range", "Content-Encoding"} {
		if len(resp.Trailer.Values(field)) != 0 {
			return nil, "unavailable"
		}
	}
	return body, ""
}

func (f *Fetcher) read(ctx context.Context, r io.Reader, length int64, body io.Writer, progress func(uint64, int64)) string {
	bound, overflow := length, "unavailable"
	if limit := f.config.MaxBytes; limit > 0 && (bound < 0 || limit <= bound) {
		bound, overflow = limit, "limit"
	}
	if progress != nil {
		progress(0, length)
	}
	var size int64
	emptyReads := 0
	var buf [32 << 10]byte
	for {
		if err := ctx.Err(); err != nil {
			return stopCode(err)
		}
		chunk := buf[:]
		// Read at most one byte past the first applicable bound.
		remaining := bound - size
		if bound >= 0 && remaining < int64(len(chunk)) {
			chunk = chunk[:remaining+1]
		}
		n, err := r.Read(chunk)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stopCode(ctxErr)
		}
		if bound >= 0 && int64(n) > remaining {
			return overflow
		}
		size += int64(n)
		if n > 0 {
			emptyReads = 0
			if written, writeErr := body.Write(buf[:n]); writeErr != nil || written != n {
				return "internal"
			}
			if progress != nil {
				progress(uint64(size), length)
			}
		} else if err == nil {
			emptyReads++
			if emptyReads == 100 {
				return "unavailable"
			}
		}
		if err == io.EOF && (length < 0 || size == length) {
			return ""
		}
		if err != nil {
			return "unavailable"
		}
	}
}
