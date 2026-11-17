package coap

import (
	"bytes"
	"context"
	"errors"
	"testing"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
)

type sourceFunc func(eris.Reference) ([]byte, error)

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (function sourceFunc) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	blockResult, blockErr := function(ref)
	return blockResult, blockErr, eris.BlockOptions{}
}

type storeFunc struct {
	get func(eris.Reference) ([]byte, error)
	put func(eris.Reference, []byte) error
}

type wrongReferenceStore struct{ eris.BlockSet }

func (wrongReferenceStore) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, _ ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, err
	}
	ref[0] ^= 1
	return ref, nil
}

type permissiveStore struct{ writes *int }

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (permissiveStore) getBlock(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	return nil, eris.ErrMissingBlock, eris.BlockOptions{}
}

func (store permissiveStore) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, _ ...eris.Reference) (eris.Reference, error) {
	(*store.writes)++
	return eris.ReferenceOf(block)
}

func (store permissiveStore) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (store storeFunc) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	blockResult, blockErr := store.get(ref)
	return blockResult, blockErr, eris.BlockOptions{}
}
func (store storeFunc) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	ref, err := eris.ReferenceOf(block)
	if err != nil {
		return eris.Reference{}, err
	}
	if len(expected) == 1 && expected[0] != ref {
		return eris.Reference{}, eris.ErrInvalidBlock
	}
	return ref, store.put(ref, block)
}

func (store storeFunc) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

type contextStore struct {
	blocks eris.BlockSet
	key    any
	value  any
	get    bool
	put    bool
}

//nolint:staticcheck // ST1008: the single-block helper keeps its return order.
func (store *contextStore) getBlock(ctx context.Context, ref eris.Reference, ttl eris.TTL, options eris.BlockOptions) ([]byte, error, eris.BlockOptions) {
	store.get = ctx.Value(store.key) == store.value
	return eris.GetBlock(ctx, store.blocks, ref, 0, eris.BlockOptions{})
}
func (store *contextStore) putBlock(ctx context.Context, block []byte, ttl eris.TTL, options eris.BlockOptions, expected ...eris.Reference) (eris.Reference, error) {
	store.put = ctx.Value(store.key) == store.value
	return eris.PutBlock(ctx, store.blocks, block, 0, eris.BlockOptions{}, expected...)
}

func (store *contextStore) updateBlockOptions(context.Context, eris.Reference, eris.TTL, eris.BlockOptions) (bool, error) {
	return false, nil
}

func serve(t *testing.T, handler protocol.Handler, request *protocol.Message) *protocol.Message {
	t.Helper()
	response, err := handler.ServeCOAP(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil {
		t.Fatal("handler returned nil response")
	}
	return response
}

func TestHandlerGet(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 7)
	blocks := eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: append([]byte(nil), block...)}}}
	handler := Handler{Path: []string{"store"}, Blocks: blocks}
	store := mustStore(t, "coap://example/store")
	request, _ := NewGetRequest(store, ref, RequestConfig{SizeHint: eris.BlockSize1KiB})
	response := serve(t, handler, request)
	if response.Code != protocol.Content || !bytes.Equal(response.Payload, block) {
		t.Fatalf("response = %s, %x", response.Code, response.Payload)
	}
	if format, _, _ := oneUintOption(response, protocol.OptionContentFormat); format != ContentFormatOctetStream {
		t.Errorf("Content-Format = %d", format)
	}
	if age, _, _ := oneUintOption(response, protocol.OptionMaxAge); age != ImmutableMaxAge {
		t.Errorf("Max-Age = %d", age)
	}
	response.Payload[0]++
	stored, _, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
	if !bytes.Equal(stored, block) {
		t.Fatal("response aliases stored block")
	}

	base32Request, _ := NewGetRequest(store, ref, RequestConfig{ReferenceEncoding: ReferenceBase32})
	if got := serve(t, handler, base32Request); got.Code != protocol.Content {
		t.Errorf("Base32 GET = %s", got.Code)
	}

	tests := []struct {
		name    string
		handler Handler
		request *protocol.Message
		code    protocol.Code
	}{
		{"bad ref", handler, &protocol.Message{Code: protocol.GET, Options: request.Options[:len(request.Options)-2]}, protocol.BadRequest},
		{"payload", handler, func() *protocol.Message { clone := request.Clone(); clone.Payload = []byte{1}; return clone }(), protocol.BadRequest},
		{"bad size", handler, func() *protocol.Message {
			clone := request.Clone()
			clone.Options[len(clone.Options)-1] = protocol.UintOption(protocol.OptionSize1, 2)
			return clone
		}(), protocol.BadRequest},
		{"non-canonical size", handler, replaceOption(request, protocol.NewOption(protocol.OptionSize1, []byte{0, 4, 0})), protocol.BadRequest},
		{"zero Hop-Limit", handler, addOption(request, protocol.UintOption(protocol.OptionHopLimit, 0)), protocol.BadRequest},
		{"large Hop-Limit", handler, addOption(request, protocol.UintOption(protocol.OptionHopLimit, 256)), protocol.BadRequest},
		{"repeated Hop-Limit", handler, func() *protocol.Message {
			clone := addOption(request, protocol.UintOption(protocol.OptionHopLimit, 2))
			return addOption(clone, protocol.UintOption(protocol.OptionHopLimit, 2))
		}(), protocol.BadRequest},
		{"missing", Handler{Path: []string{"store"}, Blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{}}}, request, protocol.NotFound},
		{"source error", Handler{Path: []string{"store"}, Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, errors.New("store") })}, request, protocol.InternalServerError},
		{"invalid stored block", Handler{Path: []string{"store"}, Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrInvalidBlock })}, request, protocol.InternalServerError},
		{"wrong stored content", Handler{Path: []string{"store"}, Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return bytes.Repeat([]byte{0}, 1024), nil })}, request, protocol.InternalServerError},
		{"oversized stored block", Handler{Path: []string{"store"}, Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return make([]byte, int(eris.BlockSize32KiB)+1), nil })}, request, protocol.InternalServerError},
		{"nil source", Handler{Path: []string{"store"}}, request, protocol.InternalServerError},
		{"method", handler, func() *protocol.Message { clone := request.Clone(); clone.Code = protocol.DELETE; return clone }(), protocol.MethodNotAllowed},
		{"nil request", handler, nil, protocol.BadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := serve(t, test.handler, test.request); got.Code != test.code {
				t.Errorf("code = %s, want %s", got.Code, test.code)
			}
		})
	}
}

func TestHandlerPut(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 8)
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	handler := Handler{Blocks: blocks, AllowPut: true}
	store := mustStore(t, "coap://example/")
	request, _ := NewPutRequest(store, block, RequestConfig{})
	if response := serve(t, handler, request); response.Code != protocol.Created || !bytes.Equal(response.Payload, ref[:]) {
		t.Fatalf("PUT response = %s, %x", response.Code, response.Payload)
	}
	stored, err, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
	if err != nil || !bytes.Equal(stored, block) {
		t.Fatalf("stored block = %x, %v", stored, err)
	}
	request.Payload[0]++
	stored, _, _ = eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
	if !bytes.Equal(stored, block) {
		t.Fatal("sink retained request payload")
	}
	withoutOptional := baseWithoutOptions(t, store, block)
	if response := serve(t, handler, withoutOptional); response.Code != protocol.Created {
		t.Errorf("PUT without optional metadata = %s", response.Code)
	}

	base, _ := NewPutRequest(store, block, RequestConfig{})
	wrongRef := ref
	wrongRef[0] ^= 1
	wrongRequest, _ := NewPutRequest(store, block, RequestConfig{}, wrongRef)
	checkedRequest, _ := NewPutRequest(store, block, RequestConfig{}, ref)
	tests := []struct {
		name    string
		handler Handler
		request *protocol.Message
		code    protocol.Code
	}{
		{"read only by default", Handler{Blocks: blocks}, base, protocol.Unauthorized},
		{"source only", Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) { return nil, eris.ErrMissingBlock }), AllowPut: true}, base, protocol.Unauthorized},
		{"bad content format", handler, replaceOption(base, protocol.UintOption(protocol.OptionContentFormat, 0)), protocol.BadRequest},
		{"repeated content format", handler, addOption(base, protocol.UintOption(protocol.OptionContentFormat, ContentFormatOctetStream)), protocol.BadRequest},
		{"bad size", handler, replaceOption(base, protocol.UintOption(protocol.OptionSize1, 2)), protocol.BadRequest},
		{"query", handler, addOption(base, protocol.StringOption(protocol.OptionURIQuery, "unexpected")), protocol.BadRequest},
		{"wrong reference", handler, wrongRequest, protocol.BadRequest},
		{"sink reports wrong reference", Handler{Blocks: wrongReferenceStore{eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}, AllowPut: true}, checkedRequest, protocol.InternalServerError},
		{"short block", handler, func() *protocol.Message {
			clone := base.Clone()
			clone.Payload = []byte("short")
			clone.Options = removeTestOption(clone.Options, protocol.OptionSize1)
			return clone
		}(), protocol.BadRequest},
		{"missing error", Handler{Blocks: storeFunc{get: func(ref eris.Reference) ([]byte, error) {
			blockResult, blockErr, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
			return blockResult, blockErr
		}, put: func(eris.Reference, []byte) error { return eris.ErrMissingBlock }}, AllowPut: true}, base, protocol.NotFound},
		{"sink error", Handler{Blocks: storeFunc{get: func(ref eris.Reference) ([]byte, error) {
			blockResult, blockErr, _ := eris.GetBlock(context.Background(), blocks, ref, 0, eris.BlockOptions{})
			return blockResult, blockErr
		}, put: func(eris.Reference, []byte) error { return errors.New("store") }}, AllowPut: true}, base, protocol.InternalServerError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := serve(t, test.handler, test.request); got.Code != test.code {
				t.Errorf("code = %s, want %s", got.Code, test.code)
			}
		})
	}
	writes := 0
	unchecked := Handler{Blocks: permissiveStore{writes: &writes}, AllowPut: true}
	if got := serve(t, unchecked, wrongRequest); got.Code != protocol.BadRequest || writes != 0 {
		t.Fatalf("wrong reference: code = %s, writes = %d", got.Code, writes)
	}
	if got := serve(t, Handler{Blocks: wrongReferenceStore{eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}}, AllowPut: true}, base); got.Code != protocol.InternalServerError {
		t.Fatalf("wrong returned reference without query: code = %s", got.Code)
	}
}

func baseWithoutOptions(t *testing.T, store StoreURL, block []byte) *protocol.Message {
	t.Helper()
	request, err := NewPutRequest(store, block, RequestConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request.Options = removeTestOption(request.Options, protocol.OptionContentFormat)
	request.Options = removeTestOption(request.Options, protocol.OptionSize1)
	return request
}

func TestHandlerRejectsUnassembledPut(t *testing.T) {
	t.Parallel()
	block, _ := makeBlock(int(eris.BlockSize1KiB), 17)
	store := mustStore(t, "coap://example/")
	for _, fragment := range []protocol.Block{
		{Number: 0, More: true, Size: 1024},
		{Number: 31, More: false, Size: 1024},
		{Number: 0, More: false, Size: 1024},
	} {
		blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
		handler := Handler{Blocks: blocks, AllowPut: true}
		request := baseWithoutOptions(t, store, block)
		option, err := fragment.Option(protocol.OptionBlock1)
		if err != nil {
			t.Fatal(err)
		}
		request = addOption(request, option)
		if response := serve(t, handler, request); response.Code != protocol.BadOption {
			t.Errorf("fragment %+v: response = %s, want BadOption", fragment, response.Code)
		}
		if len(blocks.Blocks) != 0 {
			t.Fatal("stored a fragment as a complete ERIS block")
		}
	}
}

func replaceOption(message *protocol.Message, option protocol.Option) *protocol.Message {
	clone := message.Clone()
	for index := range clone.Options {
		if clone.Options[index].Number == option.Number {
			clone.Options[index] = option
			return clone
		}
	}
	clone.Options = append(clone.Options, option)
	sortOptions(clone.Options)
	return clone
}

func addOption(message *protocol.Message, option protocol.Option) *protocol.Message {
	clone := message.Clone()
	clone.Options = append(clone.Options, option)
	sortOptions(clone.Options)
	return clone
}

func removeTestOption(options []protocol.Option, number protocol.OptionNumber) []protocol.Option {
	result := make([]protocol.Option, 0, len(options))
	for _, option := range options {
		if option.Number != number {
			result = append(result, option)
		}
	}
	return result
}

func TestHandlerContextNextAndValidation(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize1KiB), 9)
	type contextKey struct{}
	key := contextKey{}
	storage := &contextStore{blocks: eris.BlockSet{Blocks: map[eris.Reference]eris.Block{ref: {Data: block}}}, key: key, value: "value"}
	handler := Handler{Blocks: storage, AllowPut: true}
	store := mustStore(t, "coap://example/")
	get, _ := NewGetRequest(store, ref, RequestConfig{})
	put, _ := NewPutRequest(store, block, RequestConfig{})
	ctx := context.WithValue(context.Background(), key, "value")
	if response, _ := handler.ServeCOAP(ctx, get); response.Code != protocol.Content || !storage.get {
		t.Fatalf("context GET = %s, used=%v", response.Code, storage.get)
	}
	if response, _ := handler.ServeCOAP(ctx, put); response.Code != protocol.Created || !storage.put {
		t.Fatalf("context PUT = %s, used=%v", response.Code, storage.put)
	}

	sentinel := errors.New("next")
	next := protocol.HandlerFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) { return nil, sentinel })
	unmatched := &protocol.Message{Code: protocol.GET}
	if _, err := (Handler{Path: []string{"x"}, Blocks: storage, Next: next}).ServeCOAP(ctx, unmatched); !errors.Is(err, sentinel) {
		t.Errorf("Next error = %v", err)
	}
	if got := serve(t, Handler{Path: []string{"x"}, Blocks: storage}, unmatched); got.Code != protocol.NotFound {
		t.Errorf("default Next = %s", got.Code)
	}
	if err := (Handler{}).Validate(); err == nil {
		t.Fatal("nil source validated")
	}
	if err := (Handler{Path: []string{"."}, Blocks: storage}).Validate(); !errors.Is(err, ErrInvalidStoreURL) {
		t.Errorf("invalid path error = %v", err)
	}
	if _, err := (Handler{}).BlockWise(protocol.BlockWiseConfig{}); err == nil {
		t.Fatal("invalid BlockWise handler succeeded")
	}
}

func TestBlockWiseClientHandler(t *testing.T) {
	t.Parallel()
	blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
	server, err := (Handler{Blocks: blocks, AllowPut: true}).BlockWise(protocol.BlockWiseConfig{BlockSize: 1024})
	if err != nil {
		t.Fatal(err)
	}
	requester := RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
		return server.ServeCOAP(ctx, request)
	})
	client, err := NewClient(requester, mustStore(t, "coap+tcp://example/"), ClientConfig{Transfer: protocol.TransferConfig{Mode: protocol.TransferBlockWise, BlockSize: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []eris.BlockSize{eris.BlockSize1KiB, eris.BlockSize32KiB} {
		block, ref := makeBlock(int(size), 11)
		if _, err := eris.PutBlock(context.Background(), client, block, 0, eris.BlockOptions{}, ref); err != nil {
			t.Fatal(err)
		}
		got, err, _ := eris.GetBlock(context.Background(), client, ref, 0, eris.BlockOptions{})
		if err != nil || !bytes.Equal(got, block) {
			t.Fatalf("GetBlock() = %x, %v", got, err)
		}
	}
}

func TestHandlerRejectsBlock1GetBeforeStorage(t *testing.T) {
	_, ref := makeBlock(int(eris.BlockSize1KiB), 23)
	handler := Handler{Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
		t.Fatal("Block1 GET reached storage")
		return nil, nil
	})}
	request, err := NewGetRequest(mustStore(t, "coap://example/"), ref, RequestConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range [][]byte{nil, {7}, {8}, {0xff, 0xff, 0xff, 0xff}} {
		fragment := addOption(request, protocol.NewOption(protocol.OptionBlock1, value))
		if response := serve(t, handler, fragment); response.Code != protocol.BadOption {
			t.Fatalf("Block1 %x: response = %s", value, response.Code)
		}
	}
}

func TestDirectHandlerRejectsBlock2BeforeStorage(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize1KiB), 26)
	called := false
	handler := Handler{Blocks: storeFunc{
		get: func(eris.Reference) ([]byte, error) { called = true; return block, nil },
		put: func(eris.Reference, []byte) error { called = true; return nil },
	}, AllowPut: true}
	store := mustStore(t, "coap://example/")
	get, _ := NewGetRequest(store, ref, RequestConfig{})
	put, _ := NewPutRequest(store, block, RequestConfig{})
	for _, request := range []*protocol.Message{get, put} {
		for _, value := range [][]byte{nil, {7}, {0xff, 0xff, 0xff, 0xff}} {
			selected := addOption(request, protocol.NewOption(protocol.OptionBlock2, value))
			if response := serve(t, handler, selected); response.Code != protocol.BadOption {
				t.Fatalf("%s Block2 %x: response = %s", request.Code, value, response.Code)
			}
		}
	}
	if called {
		t.Fatal("unsupported Block2 reached storage")
	}
}

// BatchSize reports the maximum number of requests per call.
func (s sourceFunc) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s sourceFunc) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// BatchSize reports the maximum number of requests per call.
func (s wrongReferenceStore) BatchSize() int { return 1 }

// PutBlock processes requests in order and returns the completed prefix.
func (s wrongReferenceStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s permissiveStore) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s permissiveStore) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s permissiveStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s permissiveStore) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s storeFunc) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s storeFunc) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s storeFunc) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s storeFunc) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

// BatchSize reports the maximum number of requests per call.
func (s *contextStore) BatchSize() int { return 1 }

// GetBlock returns results in request order, with zero slots for missing blocks.
// Missing blocks do not stop later reads. See eris.BlockSource for error rules.
func (s *contextStore) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	return batch.Read(ctx, requests, s.BatchSize(), eris.ErrMissingBlock, func(r eris.BlockRequest) (eris.Block, error) {
		data, err, options := s.getBlock(ctx, r.Reference, r.TTL, r.Options)
		return eris.Block{Data: data, Options: options}, err
	})
}

// PutBlock processes requests in order and returns the completed prefix.
func (s *contextStore) PutBlock(ctx context.Context, requests []eris.PutBlockRequest) ([]eris.Reference, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.PutBlockRequest) (eris.Reference, error) {
		var expected []eris.Reference
		if r.Expected != nil {
			expected = []eris.Reference{*r.Expected}
		}
		return s.putBlock(ctx, r.Data, r.TTL, r.Options, expected...)
	})
}

// UpdateBlockOptions processes requests in order and returns the completed prefix.
func (s *contextStore) UpdateBlockOptions(ctx context.Context, requests []eris.BlockRequest) ([]bool, error) {
	return batch.Run(ctx, requests, s.BatchSize(), func(r eris.BlockRequest) (bool, error) {
		return s.updateBlockOptions(ctx, r.Reference, r.TTL, r.Options)
	})
}

func (sourceFunc) PrefetchBlock(context.Context, []eris.BlockRequest) {}

func (storeFunc) PrefetchBlock(context.Context, []eris.BlockRequest) {}

func (permissiveStore) PrefetchBlock(context.Context, []eris.BlockRequest) {}

func (*contextStore) PrefetchBlock(context.Context, []eris.BlockRequest) {}
