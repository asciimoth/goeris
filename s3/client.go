package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/asciimoth/goeris"
)

// HTTPClient executes HTTP requests. *http.Client implements HTTPClient.
// Implementations must follow the response guarantees of http.Client.Do and
// must not follow redirects. A redirect can send a session token or a PUT body
// to another host. Client disables redirects when given a *http.Client.
type HTTPClient interface {
	Do(request *http.Request) (*http.Response, error)
}

// Client reads and writes ERIS blocks in one S3 bucket.
type Client struct {
	httpClient HTTPClient
	store      StoreURL
	signer     Signer
}

// NewClient returns a client for store. signer can be nil for public reads. A
// client without a signer returns ErrAuthentication when asked to write. PUT
// requests set the public-read canned ACL required by the EER.
func NewClient(httpClient HTTPClient, store StoreURL, signer Signer) (*Client, error) {
	if httpClient == nil {
		return nil, ErrNilHTTPClient
	}
	if store.String() == "" {
		return nil, ErrInvalidStoreURL
	}
	if standard, ok := httpClient.(*http.Client); ok {
		if standard == nil {
			return nil, ErrNilHTTPClient
		}
		copy := *standard
		copy.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
		httpClient = &copy
	}
	return &Client{httpClient: httpClient, store: store, signer: signer}, nil
}

// NewRequest returns a request for ref and signs it when the client has a
// signer. It does not execute the request. Only GET, HEAD, and PUT are allowed.
func (client *Client) NewRequest(ctx context.Context, method string, ref eris.Reference, block []byte) (*http.Request, error) {
	return client.newRequest(ctx, method, ref, block, true)
}

func (client *Client) newRequest(ctx context.Context, method string, ref eris.Reference, block []byte, check bool) (*http.Request, error) {
	if client == nil || client.httpClient == nil {
		return nil, ErrNilHTTPClient
	}
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodPut {
		return nil, ErrUnsupportedMethod
	}
	if method == http.MethodPut {
		if client.signer == nil {
			return nil, ErrAuthentication
		}
		if check {
			if err := eris.VerifyBlock(block, ref); err != nil {
				return nil, err
			}
		}
	} else if len(block) != 0 {
		return nil, eris.ErrInvalidBlock
	}
	objectURL, err := client.store.ObjectURL(ref.String())
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, objectURL.String(), bytes.NewReader(block))
	if err != nil {
		return nil, err
	}
	if method == http.MethodPut {
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("x-amz-acl", "public-read")
	}
	if client.signer != nil {
		if err := client.signer.Sign(request, block); err != nil {
			return nil, err
		}
	}
	return request, nil
}

// GetBlock retrieves and verifies one block.
func (client *Client) GetBlock(ref eris.Reference) ([]byte, error) {
	return client.GetBlockContext(context.Background(), ref)
}

// GetBlockContext retrieves and verifies one block.
func (client *Client) GetBlockContext(ctx context.Context, ref eris.Reference) ([]byte, error) {
	request, err := client.NewRequest(ctx, http.MethodGet, ref, nil)
	if err != nil {
		return nil, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, eris.ErrMissingBlock
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response)
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

// PutBlock stores one block and returns its reference.
func (client *Client) PutBlock(block []byte, expected ...eris.Reference) (eris.Reference, error) {
	return client.PutBlockContext(context.Background(), block, expected...)
}

// PutBlockContext stores one block and returns its reference.
func (client *Client) PutBlockContext(ctx context.Context, block []byte, expected ...eris.Reference) (eris.Reference, error) {
	if client == nil || client.httpClient == nil {
		return eris.Reference{}, ErrNilHTTPClient
	}
	if client.signer == nil {
		return eris.Reference{}, ErrAuthentication
	}
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, errors.Join(eris.ErrInvalidBlock, err)
	}
	if len(expected) > 1 || len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	request, err := client.newRequest(ctx, http.MethodPut, ref, block, false)
	if err != nil {
		return eris.Reference{}, err
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return eris.Reference{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return eris.Reference{}, responseError(response)
	}
	limit := int64(eris.BlockSize32KiB) + 1
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, limit))
	if err != nil {
		return eris.Reference{}, err
	}
	if n == limit {
		return eris.Reference{}, ErrResponseTooLarge
	}
	return ref, nil
}

func responseError(response *http.Response) error {
	return &ResponseError{StatusCode: response.StatusCode, Status: response.Status}
}

var (
	_ eris.BlockStore         = (*Client)(nil)
	_ eris.ContextBlockSource = (*Client)(nil)
	_ eris.ContextBlockSink   = (*Client)(nil)
)
