package coap

import (
	"bytes"
	"errors"
	"net"
	"testing"

	protocol "github.com/asciimoth/coaperation"
)

func TestPrepareForward(t *testing.T) {
	t.Parallel()
	request := &protocol.Message{Type: protocol.NonConfirmable, Code: protocol.GET, MessageID: 42, Token: []byte("downstream"), RemoteAddr: &net.UDPAddr{IP: net.IPv6loopback, Port: 5683}, LocalAddr: &net.UDPAddr{IP: net.IPv6unspecified, Port: 5683}, Multicast: true, Separate: true, Options: []protocol.Option{
		protocol.StringOption(protocol.OptionURIPath, "blocks"),
		protocol.NewOption(protocol.OptionURIQuery, []byte{0, 1, 2}),
		protocol.UintOption(protocol.OptionHopLimit, 3),
		protocol.StringOption(protocol.OptionProxyURI, "ocapn://peer"),
	}}
	forwarded, err := PrepareForward(request)
	if err != nil {
		t.Fatal(err)
	}
	limit, _, _ := oneUintOption(forwarded, protocol.OptionHopLimit)
	if limit != 2 {
		t.Errorf("Hop-Limit = %d", limit)
	}
	if !bytes.Equal(optionValue(t, forwarded, protocol.OptionURIQuery), []byte{0, 1, 2}) || string(optionValue(t, forwarded, protocol.OptionProxyURI)) != "ocapn://peer" {
		t.Fatal("forwarding did not preserve routing options")
	}
	originalLimit, _, _ := oneUintOption(request, protocol.OptionHopLimit)
	if originalLimit != 3 {
		t.Fatal("PrepareForward changed input")
	}
	if forwarded.Type != protocol.Confirmable || forwarded.MessageID != 0 || forwarded.Token != nil || forwarded.LocalAddr != nil || forwarded.RemoteAddr != nil || forwarded.Multicast || forwarded.Separate {
		t.Fatalf("forwarded exchange fields = %+v", forwarded)
	}

	tests := []struct {
		request *protocol.Message
		want    error
		code    protocol.Code
	}{
		{nil, ErrInvalidHopLimit, protocol.BadRequest},
		{&protocol.Message{}, ErrNoHopLimit, protocol.NotFound},
		{&protocol.Message{Options: []protocol.Option{protocol.UintOption(protocol.OptionHopLimit, 0)}}, ErrInvalidHopLimit, protocol.BadRequest},
		{&protocol.Message{Options: []protocol.Option{protocol.NewOption(protocol.OptionHopLimit, []byte{0, 2})}}, ErrInvalidHopLimit, protocol.BadRequest},
		{&protocol.Message{Options: []protocol.Option{protocol.UintOption(protocol.OptionHopLimit, 1)}}, ErrHopLimitReached, protocol.HopLimitReached},
		{&protocol.Message{Options: []protocol.Option{protocol.UintOption(protocol.OptionHopLimit, 2), protocol.UintOption(protocol.OptionHopLimit, 2)}}, ErrInvalidHopLimit, protocol.BadRequest},
	}
	for _, test := range tests {
		_, err := PrepareForward(test.request)
		if !errors.Is(err, test.want) {
			t.Errorf("PrepareForward(%+v) error = %v, want %v", test.request, err, test.want)
		}
		response, ok := ForwardErrorResponse(err)
		if !ok || response.Code != test.code {
			t.Errorf("ForwardErrorResponse(%v) = %+v, %v", err, response, ok)
		}
	}
	if response, ok := ForwardErrorResponse(errors.New("other")); ok || response != nil {
		t.Errorf("unrelated error = %+v, %v", response, ok)
	}
}
