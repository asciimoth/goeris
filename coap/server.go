package coap

import (
	"context"
	"errors"
	"unicode/utf8"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

// Handler serves the blocks resource of one store. Path contains the decoded
// URI path segments before the final blocks segment. An empty path selects the
// endpoint-wide root store.
//
// Handler expects complete request bodies. Use BlockWise to add RFC 7959
// reassembly and response segmentation. Direct requests with a Block1 or Block2
// option are rejected with 4.02 Bad Option. Callers must not mutate Path or
// the handler fields while ServeCOAP is active.
// Unsupported critical options are rejected before storage is called.
type Handler struct {
	// Path contains the store path before its final blocks segment.
	Path []string
	// Blocks supplies reads and, when it also implements eris.BlockSink or
	// eris.ContextBlockSink, writes.
	Blocks eris.BlockSource
	// AllowPut enables PUT when Blocks also implements eris.BlockSink or
	// eris.ContextBlockSink. The zero value rejects PUT with 4.01 Unauthorized.
	AllowPut bool
	// MaxTransfers limits active and retained block-wise transfers. Zero uses
	// DefaultMaxTransfers. It applies only to the handler returned by BlockWise.
	MaxTransfers int
	// Next receives requests outside this store. A nil Next produces 4.04 Not
	// Found.
	Next protocol.Handler
}

// Validate checks the handler configuration.
func (handler Handler) Validate() error {
	if handler.Blocks == nil {
		return errors.New("eris coap: nil block source")
	}
	if handler.MaxTransfers < 0 {
		return errors.New("eris coap: negative transfer limit")
	}
	for index, segment := range handler.Path {
		if !utf8.ValidString(segment) || segment == "." || segment == ".." || index == len(handler.Path)-1 && segment == "" {
			return ErrInvalidStoreURL
		}
	}
	return nil
}

// BlockWise wraps handler with complete-body Block1 and Block2 support. A zero
// MaxBodySize limits reassembled bodies to the largest valid block size.
// Larger limits are reduced to that size.
func (handler Handler) BlockWise(config protocol.BlockWiseConfig) (*BlockWiseHandler, error) {
	if err := handler.Validate(); err != nil {
		return nil, err
	}
	if config.MaxBodySize == 0 || config.MaxBodySize > int(eris.BlockSize32KiB) {
		config.MaxBodySize = int(eris.BlockSize32KiB)
	}
	return newBlockWiseHandler(handler, config)
}

// ServeCOAP implements coaperation.Handler.
func (handler Handler) ServeCOAP(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if request == nil {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if !matchesPath(request, handler.Path, blocksSegment) {
		return handler.next(ctx, request)
	}
	if len(request.Token) > maxTokenSize {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if response, rejected := rejectCriticalOptions(request); rejected {
		return response, nil
	}
	if _, present := request.Option(protocol.OptionProxyURI); present {
		return &protocol.Message{Code: protocol.BadOption}, nil
	}
	if _, present := request.Option(protocol.OptionBlock2); present {
		return &protocol.Message{Code: protocol.BadOption}, nil
	}
	if handler.Blocks == nil {
		return &protocol.Message{Code: protocol.InternalServerError}, nil
	}
	if err := validateHopLimit(request); err != nil {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	switch request.Code {
	case protocol.GET:
		return handler.get(ctx, request), nil
	case protocol.PUT:
		return handler.put(ctx, request), nil
	default:
		return &protocol.Message{Code: protocol.MethodNotAllowed}, nil
	}
}

func (handler Handler) next(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if handler.Next != nil {
		return handler.Next.ServeCOAP(ctx, request)
	}
	return &protocol.Message{Code: protocol.NotFound}, nil
}

func (handler Handler) get(ctx context.Context, request *protocol.Message) *protocol.Message {
	if _, present := request.Option(protocol.OptionBlock1); present {
		return &protocol.Message{Code: protocol.BadOption}
	}
	if len(request.Payload) != 0 {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	ref, err := parseReferenceQuery(request)
	if err != nil {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	if size, present, sizeErr := oneUintOption(request, protocol.OptionSize1); sizeErr != nil || present && size != uint32(eris.BlockSize1KiB) && size != uint32(eris.BlockSize32KiB) {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	block, err := handler.getBlock(ctx, ref)
	if err != nil {
		if errors.Is(err, eris.ErrMissingBlock) {
			return &protocol.Message{Code: protocol.NotFound}
		}
		return &protocol.Message{Code: protocol.InternalServerError}
	}
	if err := eris.VerifyBlock(block, ref); err != nil {
		return &protocol.Message{Code: protocol.InternalServerError}
	}
	return &protocol.Message{
		Code: protocol.Content,
		Options: []protocol.Option{
			protocol.UintOption(protocol.OptionContentFormat, ContentFormatOctetStream),
			protocol.UintOption(protocol.OptionMaxAge, ImmutableMaxAge),
		},
		Payload: append([]byte(nil), block...),
	}
}

func (handler Handler) put(ctx context.Context, request *protocol.Message) *protocol.Message {
	if !handler.AllowPut {
		return &protocol.Message{Code: protocol.Unauthorized}
	}
	// A fragment can have a valid ERIS block length. Only the transfer wrapper
	// can establish that the complete body was received; it removes Block1
	// before calling this handler.
	if _, present := request.Option(protocol.OptionBlock1); present {
		return &protocol.Message{Code: protocol.BadOption}
	}
	var expected []eris.Reference
	if _, present := request.Option(protocol.OptionURIQuery); present {
		ref, err := parseReferenceQuery(request)
		if err != nil {
			return &protocol.Message{Code: protocol.BadRequest}
		}
		expected = append(expected, ref)
	}
	if value, present, err := oneUintOption(request, protocol.OptionContentFormat); err != nil || present && value != ContentFormatOctetStream {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	if size, present, err := oneUintOption(request, protocol.OptionSize1); err != nil || present && uint64(size) != uint64(len(request.Payload)) {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	if !validBlockLength(len(request.Payload)) {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	if !handler.writable() {
		return &protocol.Message{Code: protocol.Unauthorized}
	}
	actual, err := eris.ReferenceOf(request.Payload)
	if err != nil || len(expected) == 1 && expected[0] != actual {
		return &protocol.Message{Code: protocol.BadRequest}
	}
	ref, err := handler.putBlock(ctx, request.Payload, expected...)
	if err != nil {
		if errors.Is(err, eris.ErrMissingBlock) {
			return &protocol.Message{Code: protocol.NotFound}
		}
		if errors.Is(err, eris.ErrInvalidBlock) {
			return &protocol.Message{Code: protocol.BadRequest}
		}
		return &protocol.Message{Code: protocol.InternalServerError}
	}
	if ref != actual {
		return &protocol.Message{Code: protocol.InternalServerError}
	}
	return &protocol.Message{Code: protocol.Created, Payload: ref[:]}
}

func (handler Handler) getBlock(ctx context.Context, ref eris.Reference) ([]byte, error) {
	if contextual, ok := handler.Blocks.(eris.ContextBlockSource); ok {
		return contextual.GetBlockContext(ctx, ref)
	}
	return handler.Blocks.GetBlock(ref)
}

func (handler Handler) writable() bool {
	_, sink := handler.Blocks.(eris.BlockSink)
	_, contextual := handler.Blocks.(eris.ContextBlockSink)
	return sink || contextual
}

func (handler Handler) putBlock(ctx context.Context, block []byte, expected ...eris.Reference) (eris.Reference, error) {
	if contextual, ok := handler.Blocks.(eris.ContextBlockSink); ok {
		return contextual.PutBlockContext(ctx, append([]byte(nil), block...), expected...)
	}
	return handler.Blocks.(eris.BlockSink).PutBlock(append([]byte(nil), block...), expected...)
}

var _ protocol.Handler = Handler{}
