package coap

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/asciimoth/coaperation"
	"github.com/asciimoth/goeris"
)

func uploadFragment(t *testing.T, marker byte, number uint32) *protocol.Message {
	t.Helper()
	block, ref := makeBlock(int(eris.BlockSize1KiB), marker)
	request, err := NewPutRequest(mustStore(t, "coap://example/"), block, RequestConfig{}, ref)
	if err != nil {
		t.Fatal(err)
	}
	option, err := (protocol.Block{Number: number, More: number == 0, Size: 512}).Option(protocol.OptionBlock1)
	if err != nil {
		t.Fatal(err)
	}
	request = addOption(request, option)
	request.Type = protocol.Confirmable
	request.Payload = request.Payload[number*512 : (number+1)*512]
	return request
}

func TestBlockWiseTransferLimitAndCompletion(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	handler, err := (Handler{Blocks: blocks, AllowPut: true, MaxTransfers: 2}).BlockWise(protocol.BlockWiseConfig{BlockSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []byte{1, 2} {
		if response := serve(t, handler, uploadFragment(t, marker, 0)); response.Code != protocol.Continue {
			t.Fatalf("first fragment: %s", response.Code)
		}
	}
	if len(blocks) != 0 || len(handler.transfers) != 2 {
		t.Fatal("fragments were stored or transfer limit was not reached")
	}
	if response := serve(t, handler, uploadFragment(t, 3, 0)); response.Code != serviceUnavailable {
		t.Fatalf("new transfer at limit: %s", response.Code)
	}
	if response := serve(t, handler, uploadFragment(t, 1, 1)); response.Code != protocol.Created {
		t.Fatalf("existing transfer at limit: %s", response.Code)
	}
	if len(blocks) != 1 || len(handler.transfers) != 1 {
		t.Fatal("completed upload did not store a block and release its state")
	}
	if response := serve(t, handler, uploadFragment(t, 3, 0)); response.Code != protocol.Continue {
		t.Fatalf("new transfer after completion: %s", response.Code)
	}
}

func TestBlockWiseRejectsInvalidBlock2BeforeReassembly(t *testing.T) {
	blocks := make(eris.BlockSet)
	handler, err := (Handler{Blocks: blocks, AllowPut: true, MaxTransfers: 1}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range [][]byte{{7}, {15}, {0xff, 0xff, 0xff}, {0, 0, 0, 0}} {
		request := addOption(uploadFragment(t, 1, 0), protocol.NewOption(protocol.OptionBlock2, value))
		if response := serve(t, handler, request); response.Code != protocol.BadOption {
			t.Fatalf("Block2 %x: got %s, want BadOption", value, response.Code)
		}
		if len(handler.transfers) != 0 || len(blocks) != 0 {
			t.Fatal("invalid fragment retained state or reached storage")
		}
	}
	if response := serve(t, handler, uploadFragment(t, 2, 0)); response.Code != protocol.Continue {
		t.Fatalf("valid upload after invalid fragments: %s", response.Code)
	}
}

func TestConcurrentBlockWiseUploads(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	var mu sync.Mutex
	store := storeFunc{
		get: func(ref eris.Reference) ([]byte, error) {
			mu.Lock()
			defer mu.Unlock()
			return blocks.GetBlock(ref)
		},
		put: func(ref eris.Reference, block []byte) error {
			mu.Lock()
			defer mu.Unlock()
			_, err := blocks.PutBlock(block, ref)
			return err
		},
	}
	handler, err := (Handler{Blocks: store, AllowPut: true}).BlockWise(protocol.BlockWiseConfig{BlockSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	firstFragments, resume := make(chan struct{}, 2), make(chan struct{})
	requester := RequesterFunc(func(ctx context.Context, request *protocol.Message) (*protocol.Message, error) {
		response, err := handler.ServeCOAP(ctx, request)
		option, present := request.Option(protocol.OptionBlock1)
		block, _ := protocol.ParseBlock(option.Value)
		if present && block.Number == 0 {
			firstFragments <- struct{}{}
			<-resume
		}
		return response, err
	})
	client, err := NewClient(requester, mustStore(t, "coap://example/"), ClientConfig{
		Transfer: protocol.TransferConfig{Mode: protocol.TransferBlockWise, BlockSize: 512},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for _, marker := range []byte{11, 12} {
		block, _ := makeBlock(int(eris.BlockSize1KiB), marker)
		go func() {
			_, err := client.PutBlock(block)
			done <- err
		}()
	}
	// Both first fragments must reach the same resource before either upload
	// continues. Without a Request-Tag, one upload resets the other's state.
	<-firstFragments
	<-firstFragments
	close(resume)
	for range 2 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	if len(blocks) != 2 {
		t.Fatalf("stored %d blocks, want 2", len(blocks))
	}
}

func TestBlockWiseExpiry(t *testing.T) {
	t.Parallel()
	handler, err := (Handler{Blocks: make(eris.BlockSet), AllowPut: true, MaxTransfers: 1}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	serve(t, handler, uploadFragment(t, 1, 0))
	for _, transfer := range handler.transfers {
		transfer.expires = time.Now().Add(-time.Second)
	}
	if response := serve(t, handler, uploadFragment(t, 1, 1)); response.Code != protocol.RequestEntityIncomplete {
		t.Fatalf("expired upload: %s", response.Code)
	}
	if len(handler.transfers) != 0 {
		t.Fatal("failed transfer retained its state")
	}
	if response := serve(t, handler, uploadFragment(t, 2, 0)); response.Code != protocol.Continue {
		t.Fatalf("replacement upload: %s", response.Code)
	}
}

func TestBlockWiseRejectsBeforeReassembly(t *testing.T) {
	t.Parallel()
	base := uploadFragment(t, 1, 0)
	for _, test := range []struct {
		name     string
		request  *protocol.Message
		readOnly bool
		code     protocol.Code
	}{
		{"read only", base, true, protocol.Unauthorized},
		{"critical option", addOption(base, protocol.NewOption(65001, nil)), false, protocol.BadOption},
		{"duplicate Block1", addOption(base, protocol.UintOption(protocol.OptionBlock1, 0)), false, protocol.BadOption},
		{"duplicate Size1", addOption(base, protocol.UintOption(protocol.OptionSize1, 1024)), false, protocol.BadOption},
		{"long Block2", addOption(base, protocol.NewOption(protocol.OptionBlock2, make([]byte, 4))), false, protocol.BadOption},
		{"non-canonical Block2", addOption(base, protocol.NewOption(protocol.OptionBlock2, []byte{0, 8})), false, protocol.BadOption},
		{"non-canonical Block1", replaceOption(base, protocol.NewOption(protocol.OptionBlock1, []byte{0, 8})), false, protocol.BadOption},
		{"long Size2", addOption(base, protocol.NewOption(protocol.OptionSize2, make([]byte, 5))), false, protocol.BadOption},
		{"non-canonical Size2", addOption(base, protocol.NewOption(protocol.OptionSize2, []byte{0, 1})), false, protocol.BadOption},
		{"large metadata", addOption(base, protocol.NewOption(65000, make([]byte, MaxTransferKeySize))), false, protocol.BadOption},
		{"hop limit", addOption(base, protocol.UintOption(protocol.OptionHopLimit, 0)), false, protocol.BadRequest},
		{"nil", nil, false, protocol.BadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			blocks := make(eris.BlockSet)
			handler, err := (Handler{Blocks: blocks, AllowPut: !test.readOnly}).BlockWise(protocol.BlockWiseConfig{})
			if err != nil {
				t.Fatal(err)
			}
			if response := serve(t, handler, test.request); response.Code != test.code {
				t.Fatalf("response = %s, want %s", response.Code, test.code)
			}
			if len(handler.transfers) != 0 || len(blocks) != 0 {
				t.Fatal("rejected request allocated state or wrote a block")
			}
		})
	}
}

func TestBlockWiseActiveRequestIsNotExpired(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	block, ref := makeBlock(int(eris.BlockSize1KiB), 4)
	handler, err := (Handler{MaxTransfers: 1, Blocks: sourceFunc(func(eris.Reference) ([]byte, error) {
		close(entered)
		<-release
		return block, nil
	})}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := NewGetRequest(mustStore(t, "coap://example/"), ref, RequestConfig{})
	done := make(chan error, 1)
	go func() {
		response, err := handler.ServeCOAP(context.Background(), request)
		if err == nil && (response == nil || response.Code != protocol.Content) {
			err = errors.New("active read failed")
		}
		done <- err
	}()
	<-entered
	handler.mu.Lock()
	for _, transfer := range handler.transfers {
		transfer.expires = time.Now().Add(-time.Second)
	}
	handler.mu.Unlock()
	other := addOption(request, protocol.NewOption(65000, []byte("other")))
	response := serve(t, handler, other)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if response.Code != serviceUnavailable {
		t.Fatalf("active request was evicted: %s", response.Code)
	}
	if len(handler.transfers) != 0 {
		t.Fatal("single-message response retained transfer state")
	}
}

func TestBlockWiseDownloadLimit(t *testing.T) {
	t.Parallel()
	block, ref := makeBlock(int(eris.BlockSize32KiB), 5)
	handler, err := (Handler{Blocks: eris.BlockSet{ref: block}, MaxTransfers: 1}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := NewGetRequest(mustStore(t, "coap://example/"), ref, RequestConfig{})
	first := serve(t, handler, request)
	if first.Code != protocol.Content || !bytes.Equal(first.Payload, block[:1024]) {
		t.Fatal("incorrect first download fragment")
	}
	option, _ := (protocol.Block{Number: 1, Size: 1024}).Option(protocol.OptionBlock2)
	second := serve(t, handler, addOption(request, option))
	if second.Code != protocol.Content || !bytes.Equal(second.Payload, block[1024:2048]) {
		t.Fatal("download continuation failed at capacity")
	}
	if response := serve(t, handler, addOption(request, protocol.NewOption(65000, nil))); response.Code != serviceUnavailable {
		t.Fatalf("download cache exceeded capacity: %s", response.Code)
	}
	last, _ := (protocol.Block{Number: 31, Size: 1024}).Option(protocol.OptionBlock2)
	for range 2 {
		response := serve(t, handler, addOption(request, last))
		if response.Code != protocol.Content || !bytes.Equal(response.Payload, block[31*1024:]) {
			t.Fatal("final fragment or its retransmission failed")
		}
	}
	if response := serve(t, handler, addOption(request, protocol.NewOption(65000, nil))); response.Code != protocol.Content {
		t.Fatalf("completed download prevented a new transfer: %s", response.Code)
	}
}

func TestBlockWiseConfigurationAndRouting(t *testing.T) {
	t.Parallel()
	blocks := make(eris.BlockSet)
	if _, err := (Handler{Blocks: blocks, MaxTransfers: -1}).BlockWise(protocol.BlockWiseConfig{}); err == nil {
		t.Fatal("negative transfer limit accepted")
	}
	if _, err := (Handler{Blocks: blocks}).BlockWise(protocol.BlockWiseConfig{BlockSize: 7}); err == nil {
		t.Fatal("invalid transfer configuration accepted")
	}
	sentinel := errors.New("next handler")
	handler, err := (Handler{Blocks: blocks, Next: protocol.HandlerFunc(func(context.Context, *protocol.Message) (*protocol.Message, error) {
		return nil, sentinel
	})}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if handler.limit != DefaultMaxTransfers {
		t.Fatal("incorrect default transfer limit")
	}
	if _, err := handler.ServeCOAP(context.Background(), NewDiscoveryRequest()); !errors.Is(err, sentinel) {
		t.Fatalf("unmatched request: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := handler.ServeCOAP(ctx, uploadFragment(t, 1, 0)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
}

type transferAddress string

func (transferAddress) Network() string        { return "test" }
func (address transferAddress) String() string { return string(address) }

func TestBlockTransferKey(t *testing.T) {
	t.Parallel()
	base := &protocol.Message{Code: protocol.GET}
	key, ok := blockTransferKey(base)
	if !ok {
		t.Fatal("empty metadata rejected")
	}
	clone := addOption(base, protocol.UintOption(protocol.OptionBlock2, 16))
	clone.Token = []byte("new")
	if got, ok := blockTransferKey(clone); !ok || got != key {
		t.Fatal("exchange fields changed transfer identity")
	}
	for _, request := range []*protocol.Message{
		{LocalAddr: transferAddress(strings.Repeat("a", MaxTransferKeySize))},
		{RemoteAddr: transferAddress(strings.Repeat("a", MaxTransferKeySize))},
		{LocalAddr: transferAddress(strings.Repeat("a", MaxTransferKeySize-8))},
	} {
		if _, ok := blockTransferKey(request); ok {
			t.Fatal("oversized address metadata accepted")
		}
	}
	a, _ := blockTransferKey(&protocol.Message{LocalAddr: transferAddress("ab"), RemoteAddr: transferAddress("c")})
	b, _ := blockTransferKey(&protocol.Message{LocalAddr: transferAddress("a"), RemoteAddr: transferAddress("bc")})
	if a == b {
		t.Fatal("ambiguous address boundaries")
	}
}

func TestBlockWiseRejectsInvalidBodyMetadata(t *testing.T) {
	t.Parallel()
	for _, code := range []protocol.Code{protocol.PUT, protocol.GET} {
		handler, err := (Handler{Blocks: make(eris.BlockSet), AllowPut: true}).BlockWise(protocol.BlockWiseConfig{})
		if err != nil {
			t.Fatal(err)
		}
		request := uploadFragment(t, 1, 0)
		request.Code = code
		want := protocol.BadOption
		if code == protocol.PUT {
			request = replaceOption(request, protocol.NewOption(protocol.OptionSize1, []byte{0, 4, 0}))
			want = protocol.BadRequest
		}
		if response := serve(t, handler, request); response.Code != want {
			t.Fatalf("response = %s, want %s", response.Code, want)
		}
		if len(handler.transfers) != 0 {
			t.Fatal("invalid body metadata allocated transfer state")
		}
	}
}

func TestBlockWiseRejectsMetadataBeforeKeepingState(t *testing.T) {
	base := uploadFragment(t, 1, 0)
	for _, request := range []*protocol.Message{
		replaceOption(base, protocol.StringOption(protocol.OptionURIQuery, "invalid")),
		replaceOption(base, protocol.UintOption(protocol.OptionContentFormat, 0)),
		addOption(base, protocol.UintOption(protocol.OptionContentFormat, ContentFormatOctetStream)),
		replaceOption(base, protocol.UintOption(protocol.OptionSize1, 2048)),
	} {
		handler, err := (Handler{Blocks: make(eris.BlockSet), AllowPut: true}).BlockWise(protocol.BlockWiseConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if response := serve(t, handler, request); response.Code != protocol.BadRequest {
			t.Fatalf("invalid metadata: %s", response.Code)
		}
		if len(handler.transfers) != 0 {
			t.Fatal("invalid metadata retained transfer state")
		}
	}
}

func TestBlockWiseValidatesCachedDownloadRequests(t *testing.T) {
	block, ref := makeBlock(int(eris.BlockSize32KiB), 5)
	handler, err := (Handler{Blocks: eris.BlockSet{ref: block}}).BlockWise(protocol.BlockWiseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := NewGetRequest(mustStore(t, "coap://example/"), ref, RequestConfig{})
	serve(t, handler, request)
	option, _ := (protocol.Block{Number: 1, Size: 1024}).Option(protocol.OptionBlock2)
	request = addOption(request, option)
	withBody := request.Clone()
	withBody.Payload = []byte{1}
	for _, invalid := range []*protocol.Message{withBody, addOption(request, protocol.UintOption(protocol.OptionSize1, 7))} {
		if response := serve(t, handler, invalid); response.Code != protocol.BadRequest {
			t.Fatalf("invalid cached download request: %s", response.Code)
		}
	}
	if response := serve(t, handler, request); response.Code != protocol.Content {
		t.Fatalf("valid continuation: %s", response.Code)
	}
}

func TestBlockWiseBodyLimitCannotExceedERISBlock(t *testing.T) {
	handler, err := (Handler{Blocks: make(eris.BlockSet), AllowPut: true}).BlockWise(protocol.BlockWiseConfig{MaxBodySize: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for number := uint32(0); number <= 32; number++ {
		option, _ := (protocol.Block{Number: number, More: true, Size: 1024}).Option(protocol.OptionBlock1)
		request := &protocol.Message{Code: protocol.PUT, Options: []protocol.Option{
			protocol.StringOption(protocol.OptionURIPath, blocksSegment), option,
		}, Payload: make([]byte, 1024)}
		response := serve(t, handler, request)
		want := protocol.Continue
		if number == 32 {
			want = protocol.RequestEntityTooLarge
		}
		if response.Code != want {
			t.Fatalf("fragment %d: %s, want %s", number, response.Code, want)
		}
	}
	if len(handler.transfers) != 0 {
		t.Fatal("oversized upload retained state")
	}
}
