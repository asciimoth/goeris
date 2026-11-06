package http

import (
	"context"
	"fmt"
	"io"
	"mime"
	nethttp "net/http"
	"net/url"

	"github.com/asciimoth/goeris"
)

// HTTPClient executes HTTP requests. *net/http.Client implements HTTPClient.
// A custom implementation can execute requests without TCP or other network
// access.
type HTTPClient interface {
	Do(request *nethttp.Request) (*nethttp.Response, error)
}

// ResponseError reports a non-successful HTTP response.
type ResponseError struct {
	StatusCode int
	Status     string
}

// Error implements error.
func (err *ResponseError) Error() string {
	return fmt.Sprintf("eris http: response status %s", err.Status)
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
func (client *Client) NewRequest(ctx context.Context, urn string) (*nethttp.Request, error) {
	if client == nil || client.httpClient == nil {
		return nil, ErrNilHTTPClient
	}
	resourceURL, err := ResourceURL(client.baseURL, urn)
	if err != nil {
		return nil, err
	}
	return nethttp.NewRequestWithContext(ctx, nethttp.MethodGet, resourceURL.String(), nil)
}

// Resolve sends a GET request for urn. The caller must close the response body.
// Resolve returns all HTTP statuses without interpreting them.
func (client *Client) Resolve(ctx context.Context, urn string) (*nethttp.Response, error) {
	request, err := client.NewRequest(ctx, urn)
	if err != nil {
		return nil, err
	}
	return client.httpClient.Do(request)
}

// GetBlock retrieves and verifies one encrypted ERIS block. It uses a
// background context so Client implements the block-source interfaces in this
// module. Use GetBlockContext when a call needs cancellation or a deadline.
func (client *Client) GetBlock(ref eris.Reference) ([]byte, error) {
	return client.GetBlockContext(context.Background(), ref)
}

// GetBlockContext retrieves and verifies one encrypted ERIS block.
func (client *Client) GetBlockContext(ctx context.Context, ref eris.Reference) ([]byte, error) {
	response, err := client.Resolve(ctx, ref.URN())
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == nethttp.StatusNotFound {
		return nil, eris.ErrMissingBlock
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &ResponseError{StatusCode: response.StatusCode, Status: response.Status}
	}
	mediaType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || mediaType != ContentType {
		return nil, ErrUnexpectedContentType
	}
	limit := int64(eris.BlockSize32KiB) + 1
	block, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, err
	}
	if int64(len(block)) == limit {
		return nil, ErrResponseTooLarge
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err
	}
	return block, nil
}

// OpenContent retrieves remotely decoded ERIS content. The caller must close
// the returned body. OpenContent does not verify the decoded bytes.
// Applications should prefer local decoding from verified blocks when
// possible.
func (client *Client) OpenContent(ctx context.Context, capability eris.Capability) (io.ReadCloser, error) {
	if capability.Validate() != nil {
		return nil, ErrInvalidURN
	}
	response, err := client.Resolve(ctx, capability.String())
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
	return response.Body, nil
}

func cloneURL(input *url.URL) *url.URL {
	clone := *input
	if input.User != nil {
		user := *input.User
		clone.User = &user
	}
	return &clone
}

var (
	_ eris.BlockSource        = (*Client)(nil)
	_ eris.ContextBlockSource = (*Client)(nil)
)
