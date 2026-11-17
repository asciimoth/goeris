package http

import (
	"context"
	"fmt"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/internal/httpoptions"
	"github.com/asciimoth/goeris/internal/httpttl"
)

// HTTPClient executes HTTP requests. *net/http.Client implements HTTPClient.
// A custom implementation can execute requests without TCP or other network
// access. Implementations must follow the response guarantees of http.Client.Do.
type HTTPClient interface {
	Do(request *nethttp.Request) (*nethttp.Response, error)
}

// ResponseError reports a non-successful HTTP response.
type ResponseError struct {
	StatusCode int
	// Status is the raw status text from the server. It is untrusted input.
	// Error uses the standard status text instead.
	Status string
}

// Is identifies a remote forwarding limit failure.
func (err *ResponseError) Is(target error) bool {
	return target == eris.ErrTTLExpired && err.StatusCode == nethttp.StatusLoopDetected
}

// TTLHeader carries the optional request hop limit as a decimal integer from
// 1 through 255. A missing header permits local storage access only.
const TTLHeader = httpttl.Header

// HighPriorityHeader carries the optional true priority flag. A missing field
// means false. It is sent on requests and read from block responses.
const HighPriorityHeader = httpoptions.HighPriorityHeader

// RootHeader carries the optional true root flag. A missing field means false.
// It is sent on requests and read from block responses.
const RootHeader = httpoptions.RootHeader

// Error implements error.
func (err *ResponseError) Error() string {
	// The server controls Status. Do not copy it into logs or terminal output.
	return fmt.Sprintf("eris http: response status %d (%s)", err.StatusCode, nethttp.StatusText(err.StatusCode))
}

// requestError keeps request URLs and untrusted transport text out of logs.
// Callers can still inspect the cause with errors.Is and errors.As.
type requestError struct{ cause error }

func (err *requestError) Error() string { return "eris http: request failed" }
func (err *requestError) Unwrap() error { return err.cause }

// contentBody keeps transport and trailer errors out of application logs.
type contentBody struct{ io.ReadCloser }

func (body contentBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if err != nil && err != io.EOF {
		err = &requestError{cause: err}
	}
	return n, err
}

func (body contentBody) Close() error {
	if err := body.ReadCloser.Close(); err != nil {
		return &requestError{cause: err}
	}
	return nil
}

// Client retrieves ERIS resources through a caller-supplied HTTP client.
type Client struct {
	httpClient HTTPClient
	baseURL    *url.URL
}

// NewClient returns a client that resolves resources at the origin in baseURL.
// It does not change baseURL. httpClient must be created and configured by the
// caller.
func NewClient(httpClient HTTPClient, baseURL *url.URL) (*Client, error) {
	if httpClient == nil {
		return nil, ErrNilHTTPClient
	}
	if baseURL == nil {
		return nil, ErrNilBaseURL
	}
	return &Client{httpClient: httpClient, baseURL: cloneURL(baseURL)}, nil
}

// NewRequest returns a GET request for urn. It does not execute the request.
// URL parse errors are wrapped to keep capability keys out of error text.
// It sets TTLHeader to ttl.Forward(), or returns eris.ErrTTLExpired.
func (client *Client) NewRequest(ctx context.Context, urn string, ttl eris.TTL) (*nethttp.Request, error) {
	if client == nil || client.httpClient == nil {
		return nil, ErrNilHTTPClient
	}
	resourceURL, err := ResourceURL(client.baseURL, urn)
	if err != nil {
		return nil, err
	}
	request, err := nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, resourceURL.String(), nil)
	if err != nil {
		return nil, &requestError{cause: err}
	}
	if err := httpttl.Set(request.Header, ttl); err != nil {
		return nil, err
	}
	return request, nil
}

// Resolve sends a GET request for urn. The caller must close the response body.
// Resolve returns all HTTP statuses without interpreting them.
// The response body is unchanged; its errors can contain untrusted text.
// Request errors are wrapped to omit URLs and transport details from error
// text. The original cause is available through errors.Is and errors.As.
func (client *Client) Resolve(ctx context.Context, urn string, ttl eris.TTL) (*nethttp.Response, error) {
	request, err := client.NewRequest(ctx, urn, ttl)
	if err != nil {
		return nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return response, &requestError{cause: err}
	}
	return response, nil
}

// getBlock retrieves and verifies one encrypted ERIS block.
// It requires a 200 OK response with the application/octet-stream media type.
// Request and body read errors omit transport text but retain their causes.
//
//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (client *Client) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	request, err := client.NewRequest(ctx, ref.URN(), ttl)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	httpoptions.Set(request.Header, HighPriorityHeader, RootHeader, options)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &requestError{cause: err}, eris.BlockOptions{}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == nethttp.StatusNotFound {
		return nil, eris.ErrMissingBlock, eris.BlockOptions{}
	}
	if response.StatusCode != nethttp.StatusOK {
		return nil, &ResponseError{StatusCode: response.StatusCode, Status: response.Status}, eris.BlockOptions{}
	}
	mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != ContentType {
		return nil, ErrUnexpectedContentType, eris.BlockOptions{}
	}
	if response.ContentLength > int64(eris.BlockSize32KiB) {
		return nil, ErrResponseTooLarge, eris.BlockOptions{}
	}
	limit := int64(eris.BlockSize32KiB) + 1
	block, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, &requestError{cause: err}, eris.BlockOptions{}
	}
	if int64(len(block)) == limit {
		return nil, ErrResponseTooLarge, eris.BlockOptions{}
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err, eris.BlockOptions{}
	}
	return block, nil, httpoptions.Parse(response.Header, HighPriorityHeader, RootHeader)
}

// OpenContent retrieves remotely decoded ERIS content. The caller must close
// the returned body. OpenContent does not verify the decoded bytes.
// Applications should prefer local decoding from verified blocks when
// possible.
// Body read and close errors omit transport text but retain their causes.
func (client *Client) OpenContent(ctx context.Context, capability eris.Capability, ttl eris.TTL) (io.ReadCloser, error) {
	if capability.Validate() != nil {
		return nil, ErrInvalidURN
	}
	response, err := client.Resolve(ctx, capability.String(), ttl)
	if err != nil {
		return nil, err
	}
	if response.StatusCode == nethttp.StatusNotFound {
		_ = response.Body.Close()
		return nil, eris.ErrMissingBlock
	}
	if response.StatusCode != nethttp.StatusOK {
		_ = response.Body.Close()
		return nil, &ResponseError{StatusCode: response.StatusCode, Status: response.Status}
	}
	return contentBody{response.Body}, nil
}

func cloneURL(input *url.URL) *url.URL {
	clone := *input
	if input.User != nil {
		user := *input.User
		clone.User = &user
	}
	return &clone
}

var _ eris.BlockSource = (*Client)(nil)

// BatchSize returns one. This transport has no batch operation or remote
// limit discovery; one is the safe default.
func (s *Client) BatchSize() int { return 1 }

// GetBlock reads and verifies encrypted blocks, up to BatchSize requests.
// It requires a 200 OK response with the application/octet-stream media type.
// Missing blocks have zero result slots. Request and body read errors omit
// transport text but retain their causes. See eris.BlockSource for error rules.
func (s *Client) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PrefetchBlock ignores prefetch hints. This terminal source does not prefetch.
func (*Client) PrefetchBlock(context.Context, []eris.BlockRequest) {}
