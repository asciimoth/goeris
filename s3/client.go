package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/goeris/internal/httpoptions"
	"github.com/asciimoth/goeris/internal/httpttl"
)

// HTTPClient executes HTTP requests. *http.Client implements HTTPClient.
// Implementations must follow the response guarantees of http.Client.Do and
// must not follow redirects. A redirect can send a session token or a PUT body
// to another host. Client disables redirects when given a *http.Client.
type HTTPClient interface {
	Do(request *http.Request) (*http.Response, error)
}

// TTLHeader carries the optional request hop limit as a decimal integer from
// 1 through 255. It is signed with the request and is not object metadata.
// A missing header permits local storage access only.
const TTLHeader = httpttl.Header

// HighPriorityHeader carries the optional true priority flag. A missing field
// means false. It is sent on requests and read from block responses.
const HighPriorityHeader = httpoptions.S3HighPriorityHeader

// RootHeader carries the optional true root flag. A missing field means false.
// It is sent on requests and read from block responses.
const RootHeader = httpoptions.S3RootHeader

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
// It sets TTLHeader to ttl.Forward() before signing, or returns eris.ErrTTLExpired.
func (client *Client) NewRequest(ctx context.Context, method string, ref eris.Reference, block []byte, ttl eris.TTL) (*http.Request, error) {
	return client.newRequest(ctx, method, ref, block, ttl, true, eris.BlockOptions{})
}

func (client *Client) newRequest(ctx context.Context, method string, ref eris.Reference, block []byte, ttl eris.TTL, check bool, options eris.BlockOptions) (*http.Request, error) {
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
		request.Header.Set("Content-Type", ContentType)
		request.Header.Set("x-amz-acl", PublicReadACL)
	}
	if err := httpttl.Set(request.Header, ttl); err != nil {
		return nil, err
	}
	httpoptions.Set(request.Header, HighPriorityHeader, RootHeader, options)
	if client.signer != nil {
		if err := client.signer.Sign(request, block); err != nil {
			return nil, err
		}
	}
	return request, nil
}

// getBlock retrieves and verifies one block.
//
//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (client *Client) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	request, err := client.newRequest(ctx, http.MethodGet, ref, nil, ttl, true, options)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotFound {
		return nil, eris.ErrMissingBlock, eris.BlockOptions{}
	}
	if response.StatusCode != http.StatusOK {
		return nil, responseError(response), eris.BlockOptions{}
	}
	limit := int64(eris.BlockSize32KiB) + 1
	block, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	if int64(len(block)) == limit {
		return nil, ErrResponseTooLarge, eris.BlockOptions{}
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err, eris.BlockOptions{}
	}
	return block, nil, httpoptions.Parse(response.Header, HighPriorityHeader, RootHeader)
}

// putBlock stores one block and returns its reference.
func (client *Client) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
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
	request, err := client.newRequest(ctx, http.MethodPut, ref, block, ttl, false, options)
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

// updateBlockOptions does nothing because this transport has no explicit
// options-update operation. PutBlock can send options with a block.
// The batch entry point checks cancellation before each request.
func (client *Client) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

func responseError(response *http.Response) error {
	return &ResponseError{StatusCode: response.StatusCode, Status: response.Status}
}

var _ eris.BlockStore = (*Client)(nil)

// BatchSize returns one. This transport has no batch operation or remote
// limit discovery; one is the safe default.
func (s *Client) BatchSize() int { return 1 }

// GetBlock reads and verifies encrypted blocks, up to BatchSize requests.
// Each result includes the stored options. Missing blocks have zero result slots.
func (s *Client) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock verifies and stores encrypted blocks, up to BatchSize requests.
// Each Expected reference, when set, must match its block. Request options
// are sent as object metadata.
func (s *Client) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions returns false for each request because this transport
// has no explicit options-update operation. It checks cancellation before each
// request. PutBlock can send options with a block.
func (s *Client) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// PrefetchBlock ignores prefetch hints. This terminal source does not prefetch.
func (*Client) PrefetchBlock(context.Context, []eris.BlockRequest) {}
