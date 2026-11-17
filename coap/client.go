package coap

import (
	"context"
	"crypto/rand"
	"errors"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
)

// ClientConfig controls block requests. Its zero value uses raw references,
// sends 1 KiB blocks in one message, and uses block-wise transfer for 32 KiB
// blocks.
type ClientConfig struct {
	// ExperimentalHighPriority enables option 65020 on block requests and
	// responses. It is disabled by default. Use only in coordinated experiments;
	// RFC 7252 does not permit operational use of experimental option numbers.
	ExperimentalHighPriority bool
	// ExperimentalRoot enables option 65052 for root block hints. It is
	// disabled by default and is only for coordinated experiments.
	ExperimentalRoot bool
	// Request controls URI-query, size-hint, and proxy options.
	Request RequestConfig
	// Transfer controls single-message and RFC 7959 block-wise transfers.
	Transfer protocol.TransferConfig
}

// Client reads and writes blocks through a caller-supplied CoAP requester. The
// requester must route requests to the transport endpoint identified by the
// StoreURL passed to NewClient. A Client is safe for concurrent use when its
// requester is safe for concurrent use.
type Client struct {
	requester protocol.Requester
	store     StoreURL
	config    ClientConfig
}

// NewClient returns a client that uses requester for store. It performs no
// name resolution, dialing, or socket I/O.
func NewClient(requester protocol.Requester, store StoreURL, config ClientConfig) (*Client, error) {
	if requester == nil {
		return nil, ErrNilRequester
	}
	if err := store.validate(); err != nil {
		return nil, err
	}
	if config.Transfer.SingleMessageThreshold == 0 {
		// The only defined block sizes are 1 KiB and 32 KiB. Keep small blocks
		// in one message and use block-wise transfer for large blocks by default.
		config.Transfer.SingleMessageThreshold = int(eris.BlockSize1KiB)
	}
	return &Client{requester: requester, store: store, config: config}, nil
}

// getBlock retrieves and verifies one encrypted block.
//
//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (client *Client) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	if client == nil || client.requester == nil {
		return nil, ErrNilRequester, eris.BlockOptions{}
	}
	config, err := client.requestConfig(ttl)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	request, err := NewGetRequest(client.store, ref, config)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	setPriority(request, options, client.config.ExperimentalHighPriority)
	setRoot(request, options, client.config.ExperimentalRoot)
	response, err := protocol.RequestBody(ctx, &limitedRequester{
		requester: client.requester, remaining: int(eris.BlockSize32KiB), remainingOptions: MaxTransferResponseOptionSize,
	}, request, client.config.Transfer)
	if err != nil {
		return nil, err, eris.BlockOptions{}
	}
	if response.Code == protocol.NotFound {
		return nil, eris.ErrMissingBlock, eris.BlockOptions{}
	}
	if response.Code != protocol.Content {
		return nil, &ResponseError{Code: response.Code}, eris.BlockOptions{}
	}
	if err := validateContentFormat(response, ContentFormatOctetStream); err != nil {
		return nil, err, eris.BlockOptions{}
	}
	if len(response.Payload) > int(eris.BlockSize32KiB) {
		return nil, ErrResponseTooLarge, eris.BlockOptions{}
	}
	block := append([]byte(nil), response.Payload...)
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err, eris.BlockOptions{}
	}
	return block, nil, blockOptions(response, client.config.ExperimentalHighPriority, client.config.ExperimentalRoot)
}

// putBlock stores one encrypted block and returns its reference. If an
// older server returns no reference, it computes the reference locally when
// no expected reference was supplied.
func (client *Client) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	if client == nil || client.requester == nil {
		return eris.Reference{}, ErrNilRequester
	}
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, errors.Join(eris.ErrInvalidBlock, err)
	}
	if len(expected) > 1 || len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	config, err := client.requestConfig(ttl)
	if err != nil {
		return eris.Reference{}, err
	}
	request, err := NewPutRequest(client.store, block, config, expected...)
	if err != nil {
		return eris.Reference{}, err
	}
	if transfer := client.config.Transfer; transfer.Mode == protocol.TransferBlockWise ||
		transfer.Mode == protocol.TransferAuto && len(block) > transfer.SingleMessageThreshold {
		// RFC 9175 Request-Tag separates concurrent bodies sent to the same
		// resource. The exchange token alone does not identify a transfer.
		var tag [8]byte
		if _, err := rand.Read(tag[:]); err != nil {
			return eris.Reference{}, err
		}
		request.Options = append(request.Options, protocol.NewOption(292, tag[:]))
		sortOptions(request.Options)
	}
	setPriority(request, options, client.config.ExperimentalHighPriority)
	setRoot(request, options, client.config.ExperimentalRoot)
	response, err := protocol.RequestBody(ctx, &limitedRequester{
		requester: client.requester, remaining: int(eris.BlockSize32KiB), remainingOptions: MaxTransferResponseOptionSize,
	}, request, client.config.Transfer)
	if err != nil {
		return eris.Reference{}, err
	}
	if response.Code == protocol.NotFound {
		return eris.Reference{}, eris.ErrMissingBlock
	}
	if response.Code == protocol.BadRequest && len(expected) != 0 {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	if response.Code != protocol.Created {
		return eris.Reference{}, &ResponseError{Code: response.Code}
	}
	if len(response.Payload) == 0 && len(expected) == 0 {
		return ref, nil
	}
	if len(response.Payload) != len(eris.Reference{}) {
		return eris.Reference{}, ErrInvalidReference
	}
	var returned eris.Reference
	copy(returned[:], response.Payload)
	if returned != ref {
		return eris.Reference{}, ErrInvalidReference
	}
	return ref, nil
}

// updateBlockOptions does nothing because ERIS over CoAP has no explicit
// options-update operation. PutBlock can send experimental priority and root hints.
// The batch entry point checks cancellation before each request.
func (client *Client) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

// requestConfig copies configuration so concurrent calls cannot change each
// other's hop limit. An explicit operation TTL always takes precedence.
func (client *Client) requestConfig(ttl eris.TTL) (RequestConfig, error) {
	config := client.config.Request
	if ttl == eris.NoTTL && config.HopLimit != 0 {
		return config, nil
	}
	next, err := ttl.Forward()
	if err != nil {
		return RequestConfig{}, err
	}
	config.HopLimit = uint8(next)
	return config, nil
}

func validateContentFormat(message *protocol.Message, expected uint32) error {
	if message == nil {
		return errors.New("eris coap: nil response")
	}
	value, present, err := oneUintOption(message, protocol.OptionContentFormat)
	if err != nil || present && value != expected {
		return ErrUnexpectedContentFormat
	}
	return nil
}

// limitedRequester bounds response data before RequestBody reassembles it.
// Each transfer has its own instance, so concurrent calls do not share state.
type limitedRequester struct {
	requester        protocol.Requester
	remaining        int
	remainingOptions int
}

func (requester *limitedRequester) Request(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	response, err := requester.requester.Request(ctx, request)
	if err != nil || response == nil {
		return response, err
	}
	optionSize, err := validateResponseOptionsWithSize(response)
	if err != nil {
		return nil, err
	}
	if optionSize > requester.remainingOptions {
		return nil, ErrResponseTooLarge
	}
	requester.remainingOptions -= optionSize
	if option, present := response.Option(protocol.OptionSize2); present {
		// Cardinality and length were checked above. Do not start another
		// exchange for a body that cannot be a valid ERIS block.
		size, _ := option.Uint32()
		if size > uint32(eris.BlockSize32KiB) {
			return nil, ErrResponseTooLarge
		}
		if _, segmented := response.Option(protocol.OptionBlock2); !segmented && uint64(size) != uint64(len(response.Payload)) {
			// RequestBody checks Size2 only for segmented responses.
			return nil, ErrInvalidResponseOption
		}
	}
	if len(response.Payload) > requester.remaining {
		return nil, ErrResponseTooLarge
	}
	requester.remaining -= len(response.Payload)
	// Stop before RequestBody can turn a peer's TTL failure into a missing
	// Block1 acknowledgement or a changed Block2 response-code error.
	if response.Code == protocol.HopLimitReached {
		return nil, &ResponseError{Code: response.Code}
	}
	return response, nil
}

var _ eris.BlockStore = (*Client)(nil)

// BatchSize returns one. This transport has no batch operation or remote
// limit discovery; one is the safe default.
func (s *Client) BatchSize() int { return 1 }

// GetBlock reads and verifies encrypted blocks, up to BatchSize requests.
// Each result includes the stored options supported by the configured
// extensions. Missing blocks have zero result slots.
func (s *Client) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock stores encrypted blocks, up to BatchSize requests. Each Expected
// reference, when set, must match its block. If an older server returns no
// reference, the client computes it locally when no Expected value was supplied.
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
// request. PutBlock can send experimental priority and root hints.
func (s *Client) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// PrefetchBlock ignores prefetch hints. This terminal source does not prefetch.
func (*Client) PrefetchBlock(context.Context, []eris.BlockRequest) {}
