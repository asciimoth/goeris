package coap

import (
	"context"
	"crypto/rand"
	"errors"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

// ClientConfig controls block requests. Its zero value uses raw references,
// sends 1 KiB blocks in one message, and uses block-wise transfer for 32 KiB
// blocks.
type ClientConfig struct {
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

// GetBlock retrieves and verifies one encrypted block.
func (client *Client) GetBlock(ref eris.Reference) ([]byte, error) {
	return client.GetBlockContext(context.Background(), ref)
}

// GetBlockContext retrieves and verifies one encrypted block.
func (client *Client) GetBlockContext(ctx context.Context, ref eris.Reference) ([]byte, error) {
	if client == nil || client.requester == nil {
		return nil, ErrNilRequester
	}
	request, err := NewGetRequest(client.store, ref, client.config.Request)
	if err != nil {
		return nil, err
	}
	response, err := protocol.RequestBody(ctx, &limitedRequester{
		requester: client.requester, remaining: int(eris.BlockSize32KiB), remainingOptions: MaxTransferResponseOptionSize,
	}, request, client.config.Transfer)
	if err != nil {
		return nil, err
	}
	if response.Code == protocol.NotFound {
		return nil, eris.ErrMissingBlock
	}
	if response.Code != protocol.Content {
		return nil, &ResponseError{Code: response.Code}
	}
	if err := validateContentFormat(response, ContentFormatOctetStream); err != nil {
		return nil, err
	}
	if len(response.Payload) > int(eris.BlockSize32KiB) {
		return nil, ErrResponseTooLarge
	}
	block := append([]byte(nil), response.Payload...)
	if err := eris.VerifyBlock(block, ref); err != nil {
		return nil, err
	}
	return block, nil
}

// PutBlock stores one encrypted block and returns its reference.
func (client *Client) PutBlock(block []byte, expected ...eris.Reference) (eris.Reference, error) {
	return client.PutBlockContext(context.Background(), block, expected...)
}

// PutBlockContext stores one encrypted block and returns its reference. If an
// older server returns no reference, it computes the reference locally when
// no expected reference was supplied.
func (client *Client) PutBlockContext(ctx context.Context, block []byte, expected ...eris.Reference) (eris.Reference, error) {
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
	request, err := NewPutRequest(client.store, block, client.config.Request, expected...)
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
	return response, nil
}

var (
	_ eris.BlockStore         = (*Client)(nil)
	_ eris.ContextBlockSource = (*Client)(nil)
	_ eris.ContextBlockSink   = (*Client)(nil)
)
