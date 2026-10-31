package coap

import (
	"context"
	"errors"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/eris-tools/eris"
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
	response, err := protocol.RequestBody(ctx, client.requester, request, client.config.Transfer)
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

// PutBlock verifies and stores one encrypted block.
func (client *Client) PutBlock(ref eris.Reference, block []byte) error {
	return client.PutBlockContext(context.Background(), ref, block)
}

// PutBlockContext verifies and stores one encrypted block.
func (client *Client) PutBlockContext(ctx context.Context, ref eris.Reference, block []byte) error {
	if client == nil || client.requester == nil {
		return ErrNilRequester
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return err
	}
	request, err := NewPutRequest(client.store, block, client.config.Request)
	if err != nil {
		return err
	}
	response, err := protocol.RequestBody(ctx, client.requester, request, client.config.Transfer)
	if err != nil {
		return err
	}
	if response.Code == protocol.NotFound {
		return eris.ErrMissingBlock
	}
	if response.Code != protocol.Created {
		return &ResponseError{Code: response.Code}
	}
	return nil
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

var (
	_ BlockSource        = (*Client)(nil)
	_ ContextBlockSource = (*Client)(nil)
	_ BlockSink          = (*Client)(nil)
	_ ContextBlockSink   = (*Client)(nil)
)
