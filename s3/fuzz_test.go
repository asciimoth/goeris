package s3

import (
	"bufio"
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"
)

// Check the signature after HTTP serialization as well as in memory. The HTTP
// writer and parser can change escaping and header whitespace.
func FuzzSignV4WireRoundTrip(f *testing.F) {
	f.Add("https://example/a%2Fb?x=a+b", " a  b\t c ")
	f.Add("https://example/a:b?x=%20&x=+", "\u00a0value\u00a0")
	f.Add("https://[::1]:9000/", "value")
	f.Fuzz(func(t *testing.T, target, header string) {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil || request.URL.Scheme != "https" || request.URL.User != nil {
			return
		}
		request.Header.Set("X-Amz-Meta-Test", header)
		checkSignedWireRequest(t, request)
	})
}

// Callers can set URL fields directly, without the checks in url.Parse.
func FuzzSignV4WirePath(f *testing.F) {
	f.Add("/bucket/key", "", "")
	f.Add("bucket/key", "", "")
	f.Add("/bucket/key", "%2Fbucket/key", "")
	f.Add("/bucket/a/b", "/bucket/a%2Fb", "x=a+b")
	f.Add("", "", "")
	f.Add("*", "", "")
	f.Fuzz(func(t *testing.T, path, rawPath, query string) {
		request, err := http.NewRequest(http.MethodGet, "https://example/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.URL.Path = path
		request.URL.RawPath = rawPath
		request.URL.RawQuery = query
		checkSignedWireRequest(t, request)
	})
}

func checkSignedWireRequest(t *testing.T, request *http.Request) {
	t.Helper()
	if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil {
		return
	}
	var wire bytes.Buffer
	if err := request.Write(&wire); err != nil {
		t.Fatalf("signed request cannot be written: %v", err)
	}
	received, err := http.ReadRequest(bufio.NewReader(&wire))
	if err != nil {
		t.Fatalf("signed request cannot be parsed: %v", err)
	}
	defer func() { _ = received.Body.Close() }()
	auth := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "region", Clock: func() time.Time { return testTime },
	}
	if _, err := auth.Authenticate(received, nil); err != nil {
		t.Fatalf("signature failed after HTTP serialization: %v", err)
	}
}

func FuzzSignV4Authentication(f *testing.F) {
	f.Add("https://example/a%2Fb?x=1&x=2", "a  b", []byte("body"))
	f.Add("https://example/a:b?x=a+b", "\u00a0value\u00a0", []byte{})
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "us-east-1",
		Clock:  func() time.Time { return testTime },
	}
	f.Fuzz(func(t *testing.T, target, header string, payload []byte) {
		request, err := http.NewRequest(http.MethodPut, target, nil)
		if err != nil {
			return
		}
		request.Header.Set("X-Amz-Meta-Test", header)
		if err := SignV4(request, payload, testCredentials, "us-east-1", testTime); err != nil {
			return
		}
		identity, err := authenticator.Authenticate(request, payload)
		if err != nil || identity != testCredentials.AccessKey {
			t.Fatalf("signed request failed authentication: %q, %v", identity, err)
		}

		changedPath := request.Clone(context.Background())
		changedPath.URL.Path += "/tampered"
		changedPath.URL.RawPath = ""
		if _, err := authenticator.Authenticate(changedPath, payload); err == nil {
			t.Fatal("accepted a changed path")
		}
		changedHeader := request.Clone(context.Background())
		changedHeader.Header.Set("X-Amz-Meta-Test", header+"x")
		if _, err := authenticator.Authenticate(changedHeader, payload); err == nil {
			t.Fatal("accepted a changed signed header")
		}
		changedPayload := append(append([]byte(nil), payload...), 0)
		if _, err := authenticator.Authenticate(request, changedPayload); err == nil {
			t.Fatal("accepted a changed payload")
		}
	})
}
