package coap

import (
	"context"
	"encoding/binary"
	"sync"
	"time"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

// DefaultMaxTransfers is the default number of active or retained block-wise
// transfers. New transfers receive 5.03 Service Unavailable at this limit.
const DefaultMaxTransfers = 128

// MaxTransferKeySize bounds the request metadata retained for one transfer.
// Larger requests receive 4.02 Bad Option before transfer state is allocated.
const MaxTransferKeySize = 64 << 10

const serviceUnavailable protocol.Code = 5<<5 | 3

// BlockWiseHandler adds bounded Block1 and Block2 state to a block handler.
// Create it with Handler.BlockWise. It is safe for concurrent use when the
// underlying block store is safe for concurrent use. Expiry is checked on each
// request; no background tasks are started.
type BlockWiseHandler struct {
	handler   Handler
	config    protocol.BlockWiseConfig
	limit     int
	mu        sync.Mutex
	transfers map[string]*blockTransfer
}

type blockTransfer struct {
	mu       sync.Mutex
	handler  *protocol.BlockWiseHandler
	expires  time.Time
	active   int
	complete bool
}

func newBlockWiseHandler(handler Handler, config protocol.BlockWiseConfig) (*BlockWiseHandler, error) {
	if config.StateLifetime == 0 {
		config.StateLifetime = 2 * time.Minute
	}
	// Validate the transfer configuration before accepting any requests.
	if _, err := protocol.NewBlockWiseHandler(blockWiseOrigin{handler}, config); err != nil {
		return nil, err
	}
	limit := handler.MaxTransfers
	if limit == 0 {
		limit = DefaultMaxTransfers
	}
	return &BlockWiseHandler{
		handler: handler, config: config, limit: limit,
		transfers: make(map[string]*blockTransfer),
	}, nil
}

// ServeCOAP implements coaperation.Handler. It checks write permission and
// critical options before accepting fragments or allocating transfer state.
func (handler *BlockWiseHandler) ServeCOAP(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request == nil || !matchesPath(request, handler.handler.Path, BlocksResource) {
		return handler.handler.ServeCOAP(ctx, request)
	}
	// Reassembly clones the final request, including its exchange token.
	if len(request.Token) > maxTokenSize {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	if response, rejected := rejectCriticalOptions(request); rejected {
		return response, nil
	}
	if _, present := request.Option(protocol.OptionProxyURI); present {
		return &protocol.Message{Code: protocol.BadOption}, nil
	}
	if request.Code != protocol.GET && request.Code != protocol.PUT {
		return &protocol.Message{Code: protocol.MethodNotAllowed}, nil
	}
	if request.Code == protocol.PUT && (!handler.handler.AllowPut || !handler.handler.writable()) {
		return &protocol.Message{Code: protocol.Unauthorized}, nil
	}
	if err := validateHopLimit(request); err != nil {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	// The transfer helper reads only the first value and removes some options
	// after reassembly. Reject duplicates before that information is lost.
	seen := make(map[protocol.OptionNumber]bool, 4)
	var uploadBlock protocol.Block
	hasUploadBlock := false
	for _, option := range request.Options {
		maximum := 4
		switch option.Number {
		case protocol.OptionBlock1, protocol.OptionBlock2:
			maximum = 3
			// The helper can return Continue before it checks Block2. Check
			// both options here so malformed fragments cannot retain state.
			block, err := protocol.ParseBlock(option.Value)
			if err != nil {
				return &protocol.Message{Code: protocol.BadOption}, nil
			}
			if option.Number == protocol.OptionBlock1 {
				uploadBlock, hasUploadBlock = block, true
			}
		case protocol.OptionSize1, protocol.OptionSize2:
		default:
			continue
		}
		if seen[option.Number] || len(option.Value) > maximum || option.Number != protocol.OptionSize1 && !canonicalUint(option.Value) {
			return &protocol.Message{Code: protocol.BadOption}, nil
		}
		seen[option.Number] = true
	}
	if size, present, err := oneUintOption(request, protocol.OptionSize1); err != nil || present && size != uint32(eris.BlockSize1KiB) && size != uint32(eris.BlockSize32KiB) {
		return &protocol.Message{Code: protocol.BadRequest}, nil
	}
	// GET bodies are not supported. Reject fragments before retaining them.
	if request.Code == protocol.GET {
		if _, present := request.Option(protocol.OptionBlock1); present {
			return &protocol.Message{Code: protocol.BadOption}, nil
		}
		if len(request.Payload) != 0 {
			return &protocol.Message{Code: protocol.BadRequest}, nil
		}
	}
	// Check application metadata before a fragment can reserve a transfer
	// slot or a continuation can read a cached response.
	if _, present := request.Option(protocol.OptionURIQuery); request.Code == protocol.GET || present {
		if _, err := parseReferenceQuery(request); err != nil {
			return &protocol.Message{Code: protocol.BadRequest}, nil
		}
	}
	if request.Code == protocol.PUT {
		if err := validateContentFormat(request, ContentFormatOctetStream); err != nil {
			return &protocol.Message{Code: protocol.BadRequest}, nil
		}
	}
	if len(request.Payload) > int(eris.BlockSize32KiB) {
		return &protocol.Message{Code: protocol.RequestEntityTooLarge}, nil
	}
	if hasUploadBlock && (len(request.Payload) > uploadBlock.Size || uploadBlock.More && len(request.Payload) != uploadBlock.Size) {
		return &protocol.Message{Code: protocol.RequestEntityIncomplete}, nil
	}
	key, ok := blockTransferKey(request)
	if !ok {
		return &protocol.Message{Code: protocol.BadOption}, nil
	}
	handler.mu.Lock()
	now := time.Now()
	for key, transfer := range handler.transfers {
		if transfer.active == 0 && !now.Before(transfer.expires) {
			delete(handler.transfers, key)
		}
	}
	transfer := handler.transfers[key]
	if transfer == nil {
		// A completed download remains available for retransmission until
		// expiry, but must not prevent a new transfer when the cache is full.
		if len(handler.transfers) >= handler.limit {
			var oldestKey string
			var oldest *blockTransfer
			for candidateKey, candidate := range handler.transfers {
				if candidate.active == 0 && candidate.complete && (oldest == nil || candidate.expires.Before(oldest.expires)) {
					oldestKey, oldest = candidateKey, candidate
				}
			}
			if oldest != nil {
				delete(handler.transfers, oldestKey)
			}
		}
		if len(handler.transfers) >= handler.limit {
			handler.mu.Unlock()
			return &protocol.Message{Code: serviceUnavailable}, nil
		}
		inner, _ := protocol.NewBlockWiseHandler(blockWiseOrigin{handler.handler}, handler.config)
		transfer = &blockTransfer{handler: inner}
		handler.transfers[key] = transfer
	}
	transfer.active++
	handler.mu.Unlock()

	// Each helper receives exactly one transfer key. Discarding it releases
	// both its inbound body and outbound cache, including on malformed input.
	transfer.mu.Lock()
	var response *protocol.Message
	err := ctx.Err()
	if err == nil {
		response, err = transfer.handler.ServeCOAP(ctx, request)
	}
	handler.mu.Lock()
	transfer.active--
	transfer.expires = time.Now().Add(handler.config.StateLifetime)
	keep := response != nil && response.Code == protocol.Continue
	transfer.complete = false
	if response != nil {
		option, segmented := response.Option(protocol.OptionBlock2)
		keep = keep || segmented
		if segmented {
			block, parseErr := protocol.ParseBlock(option.Value)
			transfer.complete = parseErr == nil && !block.More
		}
	}
	if transfer.active == 0 && (err != nil || !keep) {
		delete(handler.transfers, key)
	}
	handler.mu.Unlock()
	transfer.mu.Unlock()
	return response, err
}

// The transfer helper owns Block2. Remove it only when the helper calls the
// complete-body handler; the direct handler must reject unsupported Block2.
type blockWiseOrigin struct{ Handler }

func (origin blockWiseOrigin) ServeCOAP(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
	if _, present := request.Option(protocol.OptionBlock2); !present {
		return origin.Handler.ServeCOAP(ctx, request)
	}
	complete := request.Clone()
	complete.Options = complete.Options[:0]
	for _, option := range request.Options {
		if option.Number != protocol.OptionBlock2 {
			complete.Options = append(complete.Options, option)
		}
	}
	return origin.Handler.ServeCOAP(ctx, complete)
}

// Match the helper's identity fields, including option order. Tokens identify
// exchanges, and can change between fragments of one transfer. Length prefixes
// keep addresses and option values unambiguous.
func blockTransferKey(request *protocol.Message) (string, bool) {
	var key []byte
	appendString := func(value string) bool {
		if len(value) > MaxTransferKeySize-len(key)-4 {
			return false
		}
		key = binary.BigEndian.AppendUint32(key, uint32(len(value)))
		key = append(key, value...)
		return true
	}
	localNetwork, local, remoteNetwork, remote := "", "", "", ""
	if request.LocalAddr != nil {
		localNetwork = request.LocalAddr.Network()
		local = request.LocalAddr.String()
	}
	if request.RemoteAddr != nil {
		remoteNetwork = request.RemoteAddr.Network()
		remote = request.RemoteAddr.String()
	}
	if !appendString(localNetwork) || !appendString(local) || !appendString(remoteNetwork) || !appendString(remote) || len(key) == MaxTransferKeySize {
		return "", false
	}
	key = append(key, byte(request.Code))
	for _, option := range request.Options {
		switch option.Number {
		case protocol.OptionBlock1, protocol.OptionBlock2, protocol.OptionSize1, protocol.OptionSize2:
			continue
		}
		if len(option.Value) > MaxTransferKeySize-len(key)-8 {
			return "", false
		}
		key = binary.BigEndian.AppendUint32(key, uint32(option.Number))
		key = binary.BigEndian.AppendUint32(key, uint32(len(option.Value)))
		key = append(key, option.Value...)
	}
	return string(key), true
}

var _ protocol.Handler = (*BlockWiseHandler)(nil)
