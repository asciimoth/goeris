package s3

import (
	"context"
	"net/http"
	"testing"
	"time"
)

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
