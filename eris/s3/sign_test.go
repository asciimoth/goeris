package s3

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

var (
	testTime        = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	testCredentials = Credentials{
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	}
)

func TestSignV4AWSExample(t *testing.T) {
	t.Parallel()
	request, err := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-9")
	if err := SignV4(request, nil, testCredentials, "us-east-1", testTime); err != nil {
		t.Fatal(err)
	}
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := request.Header.Get("Authorization"); got != want {
		t.Fatalf("Authorization = %q\nwant %q", got, want)
	}
	if request.Header.Get("x-amz-content-sha256") != sha256Hex(nil) ||
		request.Header.Get("x-amz-date") != "20130524T000000Z" {
		t.Fatal("required signing headers are missing")
	}
}

func TestCanonicalRequest(t *testing.T) {
	t.Parallel()
	request, _ := http.NewRequest(http.MethodGet, "https://example/a b?z=a+b&a=2&a=1&empty", nil)
	request.Host = " override.example "
	request.Header["X-Amz-Meta-Test"] = []string{" a  b ", "c"}
	request.Header.Set("User-Agent", "ignored")
	request.Header.Set("Authorization", "ignored")
	payloadHash := sha256Hex([]byte("payload"))
	canonical, headers, err := CanonicalRequest(request, payloadHash)
	if err != nil {
		t.Fatal(err)
	}
	if headers != "host;x-amz-meta-test" {
		t.Fatalf("headers = %q", headers)
	}
	want := "GET\n/a%20b\na=1&a=2&empty=&z=a%20b\nhost:override.example\nx-amz-meta-test:a b,c\n\nhost;x-amz-meta-test\n" + payloadHash
	if canonical != want {
		t.Fatalf("canonical request =\n%s\nwant\n%s", canonical, want)
	}
	request.Host = ""
	request.URL.Host = ""
	if got := requestHost(request); got != "" {
		t.Errorf("empty request host = %q", got)
	}
	request.URL = nil
	if got := requestHost(request); got != "" {
		t.Errorf("nil URL host = %q", got)
	}
	if got := canonicalURI(""); got != "/" {
		t.Errorf("empty canonical URI = %q", got)
	}
	values := make(url.Values)
	values["empty"] = nil
	if got := canonicalQuery(values); got != "empty=" {
		t.Errorf("empty canonical query value = %q", got)
	}
}

func TestSignatureCoversEscapedPath(t *testing.T) {
	t.Parallel()
	request, err := http.NewRequest(http.MethodGet, "https://example/bucket/a%2Fb", nil)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := CanonicalRequest(request, sha256Hex(nil))
	if err != nil || !strings.HasPrefix(canonical, "GET\n/bucket/a%2Fb\n") {
		t.Fatalf("escaped path changed: %q, %v", canonical, err)
	}
	if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil {
		t.Fatal(err)
	}
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "region",
		Clock:  func() time.Time { return testTime },
	}
	if _, err := authenticator.Authenticate(request, nil); err != nil {
		t.Fatal(err)
	}
	request.URL.RawPath = ""
	if _, err := authenticator.Authenticate(request, nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("changed escaped path authenticated: %v", err)
	}
	request.URL.RawPath = "/bucket/a%2Fb"
	request.URL.Opaque = "//example/other"
	if _, err := authenticator.Authenticate(request, nil); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("opaque target authenticated: %v", err)
	}
	if _, _, err := CanonicalRequest(request, sha256Hex(nil)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("opaque canonical request: %v", err)
	}
	if err := SignV4(request, nil, testCredentials, "region", testTime); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("opaque target signed: %v", err)
	}
}

func TestMalformedQueryCannotBeSignedOrAuthenticated(t *testing.T) {
	t.Parallel()
	request, err := http.NewRequest(http.MethodGet, "https://example/key?good=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "region",
		Clock:  func() time.Time { return testTime },
	}
	if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(request, nil); err != nil {
		t.Fatalf("valid query authentication = %v", err)
	}
	request.URL.RawQuery += "&bad=%"
	if _, err := authenticator.Authenticate(request, nil); !errors.Is(err, ErrAuthentication) {
		t.Errorf("malformed query authentication = %v", err)
	}
	if _, _, err := CanonicalRequest(request, sha256Hex(nil)); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("malformed canonical request = %v", err)
	}
	if err := SignV4(request, nil, testCredentials, "region", testTime); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("malformed query signing = %v", err)
	}
}

func TestSignAndAuthenticate(t *testing.T) {
	t.Parallel()
	credentials := testCredentials
	credentials.SessionToken = "TOKEN"
	request, _ := http.NewRequest(http.MethodPut, "https://s3.example/bucket/key", strings.NewReader("body"))
	request.Header.Set("Content-Type", " application/octet-stream ")
	signer := StaticV4Signer{Credentials: credentials, Region: "test-1", Clock: func() time.Time { return testTime }}
	if err := signer.Sign(request, []byte("body")); err != nil {
		t.Fatal(err)
	}
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(_ context.Context, accessKey string) (Credentials, error) {
			if accessKey != credentials.AccessKey {
				return Credentials{}, errors.New("unknown")
			}
			return credentials, nil
		}),
		Region: "test-1",
		Clock:  func() time.Time { return testTime.Add(2 * time.Minute) },
	}
	identity, err := authenticator.Authenticate(request, []byte("body"))
	if err != nil || identity != credentials.AccessKey {
		t.Fatalf("Authenticate() = %q, %v", identity, err)
	}

	mutations := []func(*http.Request){
		func(req *http.Request) { req.Header.Set("x-amz-date", "bad") },
		func(req *http.Request) { req.Header.Set("x-amz-security-token", "bad") },
		func(req *http.Request) { req.Header.Set("x-amz-unsigned", "value") },
		func(req *http.Request) { req.Header.Set("Authorization", "bad") },
		func(req *http.Request) { req.URL.Path = "/tampered" },
	}
	for index, mutate := range mutations {
		clone := request.Clone(context.Background())
		mutate(clone)
		if _, err := authenticator.Authenticate(clone, []byte("body")); !errors.Is(err, ErrAuthentication) {
			t.Errorf("mutation %d: %v", index, err)
		}
	}
	if _, err := authenticator.Authenticate(request, []byte("other")); !errors.Is(err, ErrAuthentication) {
		t.Errorf("payload mutation: %v", err)
	}
	expired := authenticator
	expired.Clock = func() time.Time { return testTime.Add(time.Hour) }
	if _, err := expired.Authenticate(request, []byte("body")); !errors.Is(err, ErrAuthentication) {
		t.Errorf("expired request: %v", err)
	}
}

func TestSignedHeaderPreservesInternalWhitespace(t *testing.T) {
	t.Parallel()
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "region",
		Clock:  func() time.Time { return testTime },
	}
	for name, separator := range map[string]string{
		"non-breaking space": "\u00a0", "em space": "\u2003", "tab": "\t",
	} {
		t.Run(name, func(t *testing.T) {
			request, err := http.NewRequest(http.MethodGet, "https://example/key", nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("x-amz-meta-label", "first"+separator+"second")
			canonical, _, err := CanonicalRequest(request, sha256Hex(nil))
			if err != nil || !strings.Contains(canonical, "x-amz-meta-label:first"+separator+"second\n") {
				t.Fatalf("header value changed in canonical request: %q, %v", canonical, err)
			}
			if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil {
				t.Fatal(err)
			}
			if _, err := authenticator.Authenticate(request, nil); err != nil {
				t.Fatalf("original header: %v", err)
			}
			request.Header.Set("x-amz-meta-label", "first second")
			if _, err := authenticator.Authenticate(request, nil); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("changed header authenticated: %v", err)
			}
		})
	}
}

func TestSigningErrors(t *testing.T) {
	t.Parallel()
	request, _ := http.NewRequest(http.MethodGet, "https://example/", nil)
	for _, test := range []struct {
		request     *http.Request
		credentials Credentials
		region      string
	}{
		{request, Credentials{}, "region"},
		{request, testCredentials, ""},
		{request, Credentials{AccessKey: "bad/key", SecretKey: "secret"}, "region"},
		{request, testCredentials, "bad/region"},
	} {
		if err := SignV4(test.request, nil, test.credentials, test.region, testTime); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("SignV4() = %v", err)
		}
	}
	if err := SignV4(nil, nil, testCredentials, "region", testTime); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("nil request = %v", err)
	}
	request.Header = nil
	if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil || request.Header == nil {
		t.Errorf("nil request headers = %v, %v", request.Header, err)
	}
	for _, test := range []struct {
		name    string
		request *http.Request
		hash    string
	}{
		{"nil request", nil, sha256Hex(nil)},
		{"bad hash", request, "BAD"},
		{"uppercase hash", request, strings.ToUpper(sha256Hex(nil))},
	} {
		if _, _, err := CanonicalRequest(test.request, test.hash); !errors.Is(err, ErrInvalidRequest) {
			t.Errorf("%s: %v", test.name, err)
		}
	}
	if _, err := (V4Authenticator{}).Authenticate(request, nil); !errors.Is(err, ErrAuthentication) {
		t.Errorf("Authenticate() = %v", err)
	}
}

func TestDefaultClocks(t *testing.T) {
	t.Parallel()
	request, _ := http.NewRequest(http.MethodGet, "https://example/key", nil)
	signer := StaticV4Signer{Credentials: testCredentials, Region: "region"}
	if err := signer.Sign(request, nil); err != nil {
		t.Fatal(err)
	}
	authenticator := V4Authenticator{
		Credentials: CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			return testCredentials, nil
		}),
		Region: "region",
	}
	if identity, err := authenticator.Authenticate(request, nil); err != nil || identity != testCredentials.AccessKey {
		t.Fatalf("Authenticate() = %q, %v", identity, err)
	}
}

func TestAuthenticationErrors(t *testing.T) {
	t.Parallel()
	request, _ := http.NewRequest(http.MethodGet, "https://example/key", nil)
	if err := SignV4(request, nil, testCredentials, "region", testTime); err != nil {
		t.Fatal(err)
	}
	makeAuth := func(source CredentialSource) V4Authenticator {
		return V4Authenticator{Credentials: source, Region: "region", Clock: func() time.Time { return testTime }}
	}
	tests := []struct {
		name   string
		change func(*http.Request) string
		source CredentialSource
	}{
		{"source error", func(*http.Request) string { return "" }, CredentialSourceFunc(func(context.Context, string) (Credentials, error) { return Credentials{}, errors.New("lookup") })},
		{"empty credentials", func(*http.Request) string { return "" }, CredentialSourceFunc(func(context.Context, string) (Credentials, error) { return Credentials{}, nil })},
		{"wrong access key", func(*http.Request) string { return "" }, CredentialSourceFunc(func(context.Context, string) (Credentials, error) {
			value := testCredentials
			value.AccessKey = "other"
			return value, nil
		})},
		{"wrong scope", func(req *http.Request) string {
			return strings.Replace(req.Header.Get("Authorization"), "/region/", "/other/", 1)
		}, CredentialSourceFunc(func(context.Context, string) (Credentials, error) { return testCredentials, nil })},
		{"negative skew", func(*http.Request) string { return "" }, CredentialSourceFunc(func(context.Context, string) (Credentials, error) { return testCredentials, nil })},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clone := request.Clone(context.Background())
			if changed := test.change(clone); changed != "" {
				clone.Header.Set("Authorization", changed)
			}
			auth := makeAuth(test.source)
			if test.name == "negative skew" {
				auth.ClockSkew = -1
			}
			if _, err := auth.Authenticate(clone, nil); !errors.Is(err, ErrAuthentication) {
				t.Fatalf("Authenticate() = %v", err)
			}
		})
	}
}

func TestAuthorizationAndCanonicalVerificationErrors(t *testing.T) {
	t.Parallel()
	badAuthorization := []string{
		algorithm + " Credential=key/scope,SignedHeaders=host",
		algorithm + " Credential=/scope,SignedHeaders=host,Signature=" + strings.Repeat("0", 64),
		algorithm + " Credential=key/scope,SignedHeaders=host,Signature=short",
		algorithm + " Credential=key/scope,SignedHeaders=host,Signature=" + strings.Repeat("z", 64),
	}
	for _, value := range badAuthorization {
		if _, err := parseAuthorization(value); !errors.Is(err, ErrAuthentication) {
			t.Errorf("parseAuthorization(%q) = %v", value, err)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, "https://example/key", nil)
	request.Header.Set("x-amz-date", testTime.Format(amzDateFormat))
	request.Header.Set("x-amz-content-sha256", sha256Hex(nil))
	badHeaders := []string{
		"", "x-amz-date;host", "host;host;x-amz-content-sha256;x-amz-date",
		"Host;x-amz-content-sha256;x-amz-date", "host;missing;x-amz-content-sha256;x-amz-date",
		"host;x-amz-date", "host;x-amz-content-sha256",
	}
	for _, names := range badHeaders {
		if _, _, err := canonicalRequestForVerification(request, names); !errors.Is(err, ErrAuthentication) {
			t.Errorf("canonicalRequestForVerification(%q) = %v", names, err)
		}
	}
	request.Header["X-Amz-Meta-Many"] = []string{"one", "two"}
	canonical, _, err := canonicalRequestForVerification(request, "host;x-amz-content-sha256;x-amz-date;x-amz-meta-many")
	if err != nil || !strings.Contains(canonical, "x-amz-meta-many:one,two") {
		t.Errorf("multiple values = %q, %v", canonical, err)
	}
}

func FuzzCanonicalRequest(f *testing.F) {
	f.Add("/bucket/key", "a=1&b=two", "value")
	f.Add("/a b", "x=a+b", " a  b ")
	f.Fuzz(func(t *testing.T, path, query, header string) {
		request, err := http.NewRequest(http.MethodGet, "https://example/", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.URL.Path = path
		request.URL.RawQuery = query
		request.Header.Set("x-amz-meta-fuzz", header)
		first, firstHeaders, firstErr := CanonicalRequest(request, sha256Hex(nil))
		second, secondHeaders, secondErr := CanonicalRequest(request, sha256Hex(nil))
		if _, queryErr := url.ParseQuery(query); queryErr != nil {
			if !errors.Is(firstErr, ErrInvalidRequest) || !errors.Is(secondErr, ErrInvalidRequest) {
				t.Fatalf("malformed query errors = %v, %v", firstErr, secondErr)
			}
			return
		}
		if firstErr != nil || secondErr != nil {
			t.Fatalf("canonical errors = %v, %v", firstErr, secondErr)
		}
		if first != second || firstHeaders != secondHeaders {
			t.Fatal("canonical request is not deterministic")
		}
	})
}

func FuzzAuthorizationParser(f *testing.F) {
	f.Add("AWS4-HMAC-SHA256 Credential=key/scope,SignedHeaders=host,Signature=" + strings.Repeat("0", 64))
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		parsed, err := parseAuthorization(value)
		if err == nil && (parsed.accessKey == "" || parsed.scope == "" || len(parsed.signature) != 64) {
			t.Fatalf("invalid success: %#v", parsed)
		}
	})
}
