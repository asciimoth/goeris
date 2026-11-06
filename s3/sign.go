package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	algorithm       = "AWS4-HMAC-SHA256"
	requestType     = "aws4_request"
	service         = "s3"
	amzDateFormat   = "20060102T150405Z"
	shortDateFormat = "20060102"
)

// Credentials contains one SigV4 identity. SessionToken is optional. AccessKey
// must not contain whitespace or SigV4 field delimiters.
type Credentials struct {
	AccessKey    string
	SecretKey    string
	SessionToken string
}

// Valid reports whether the required credential fields are present.
func (credentials Credentials) Valid() bool {
	return credentials.AccessKey != "" && credentials.SecretKey != "" &&
		!strings.ContainsAny(credentials.AccessKey, "/, \t\r\n")
}

// Signer adds authentication to an HTTP request. payload contains the complete
// request body. Implementations must be safe for concurrent use.
type Signer interface {
	Sign(request *http.Request, payload []byte) error
}

// StaticV4Signer signs requests with fixed credentials. Clock must be safe for
// concurrent use. time.Now is used when Clock is nil.
type StaticV4Signer struct {
	Credentials Credentials
	Region      string
	Clock       func() time.Time
}

// Sign implements Signer.
func (signer StaticV4Signer) Sign(request *http.Request, payload []byte) error {
	clock := signer.Clock
	if clock == nil {
		clock = time.Now
	}
	return SignV4(request, payload, signer.Credentials, signer.Region, clock())
}

// SignV4 adds an AWS Signature Version 4 Authorization header. It signs the
// host and all stable request headers. Its result is deterministic for
// the supplied request, payload, credentials, region, and time.
// The URL must use Path and optional RawPath, not Opaque. Header map keys
// must not contain multiple case variants of the same field.
func SignV4(request *http.Request, payload []byte, credentials Credentials, region string, now time.Time) error {
	if !credentials.Valid() || region == "" || strings.ContainsAny(region, "/, \t\r\n") {
		return ErrInvalidCredentials
	}
	if request == nil || request.URL == nil || request.Method == "" || requestHost(request) == "" ||
		!validRequestURL(request) {
		return ErrInvalidRequest
	}
	headers, err := normalizedHeaders(request.Header)
	if err != nil {
		return err
	}
	request.Header = headers
	request.Header.Del("Authorization")
	now = now.UTC()
	payloadHash := sha256Hex(payload)
	request.Header.Set("x-amz-date", now.Format(amzDateFormat))
	request.Header.Set("x-amz-content-sha256", payloadHash)
	if credentials.SessionToken != "" {
		request.Header.Set("x-amz-security-token", credentials.SessionToken)
	} else {
		request.Header.Del("x-amz-security-token")
	}
	canonical, signedHeaders := canonicalRequest(request, payloadHash)
	scope := credentialScope(now, region)
	stringToSign := algorithm + "\n" + now.Format(amzDateFormat) + "\n" + scope + "\n" + sha256Hex([]byte(canonical))
	signature := signature(credentials.SecretKey, now, region, stringToSign)
	request.Header.Set("Authorization", algorithm+" Credential="+credentials.AccessKey+"/"+scope+
		",SignedHeaders="+signedHeaders+",Signature="+signature)
	return nil
}

// CanonicalRequest returns the SigV4 canonical request and signed-header list.
// The caller must supply a lowercase hexadecimal SHA-256 payload hash.
// The URL must use Path and optional RawPath, not Opaque. Header map keys
// must not contain multiple case variants of the same field.
func CanonicalRequest(request *http.Request, payloadHash string) (string, string, error) {
	if request == nil || request.URL == nil || request.Method == "" || requestHost(request) == "" ||
		!validSHA256Hex(payloadHash) || !validRequestURL(request) {
		return "", "", ErrInvalidRequest
	}
	headers, err := normalizedHeaders(request.Header)
	if err != nil {
		return "", "", err
	}
	clone := *request
	clone.Header = headers
	canonical, signedHeaders := canonicalRequest(&clone, payloadHash)
	return canonical, signedHeaders, nil
}

// Normalize direct map assignments before using Header.Get or Header.Values.
// Distinct keys for the same field are ambiguous to callers that use Get.
func normalizedHeaders(input http.Header) (http.Header, error) {
	output := make(http.Header, len(input))
	for name, values := range input {
		canonical := http.CanonicalHeaderKey(name)
		if _, exists := output[canonical]; exists {
			return nil, ErrInvalidRequest
		}
		output[canonical] = append([]string(nil), values...)
	}
	return output, nil
}

func canonicalRequest(request *http.Request, payloadHash string) (string, string) {
	names := signedHeaderNames(request)
	var headers strings.Builder
	for _, name := range names {
		headers.WriteString(name)
		headers.WriteByte(':')
		if name == "host" {
			headers.WriteString(normalizeHeader(requestHost(request)))
		} else {
			values := request.Header.Values(name)
			for index, value := range values {
				if index != 0 {
					headers.WriteByte(',')
				}
				headers.WriteString(normalizeHeader(value))
			}
		}
		headers.WriteByte('\n')
	}
	signedHeaders := strings.Join(names, ";")
	canonical := request.Method + "\n" + canonicalURI(request.URL.EscapedPath()) + "\n" +
		canonicalQuery(request.URL.Query()) + "\n" + headers.String() + "\n" + signedHeaders + "\n" + payloadHash
	return canonical, signedHeaders
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validRequestURL(request *http.Request) bool {
	// Opaque URLs can send a request target that differs from EscapedPath.
	if request.URL.Opaque != "" {
		return false
	}
	_, err := url.ParseQuery(request.URL.RawQuery)
	return err == nil
}

func signedHeaderNames(request *http.Request) []string {
	names := []string{"host"}
	seen := map[string]bool{"host": true}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if ignoredHeader(lower) {
			continue
		}
		if !seen[lower] {
			names = append(names, lower)
			seen[lower] = true
		}
	}
	sort.Strings(names)
	return names
}

func ignoredHeader(name string) bool {
	switch name {
	case "authorization", "connection", "content-length", "expect", "transfer-encoding", "user-agent":
		return true
	default:
		return false
	}
}

func requestHost(request *http.Request) string {
	if request.Host != "" {
		return request.Host
	}
	if request.URL != nil {
		return request.URL.Host
	}
	return ""
}

func normalizeHeader(value string) string {
	// SigV4 collapses consecutive ASCII spaces inside a header value. Other
	// whitespace is data and must remain covered by the signature. Only HTTP
	// space and tab characters can be removed from the ends.
	return strings.Join(strings.FieldsFunc(strings.Trim(value, " \t"), func(char rune) bool {
		return char == ' '
	}), " ")
}

func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	// EscapedPath permits reserved characters that SigV4 requires encoded.
	// Keep existing escapes (including encoded slashes) and path segments;
	// decoding or cleaning the path can change the requested object.
	var encoded strings.Builder
	for path != "" {
		index := strings.IndexByte(path, '%')
		if index < 0 || index+2 >= len(path) {
			encoded.WriteString(uriEncode(path, false))
			break
		}
		encoded.WriteString(uriEncode(path[:index], false))
		encoded.WriteString(strings.ToUpper(path[index : index+3]))
		path = path[index+3:]
	}
	return encoded.String()
}

func canonicalQuery(values url.Values) string {
	type pair struct{ key, value string }
	pairs := make([]pair, 0)
	for key, list := range values {
		if len(list) == 0 {
			list = []string{""}
		}
		for _, value := range list {
			pairs = append(pairs, pair{uriEncode(key, true), uriEncode(value, true)})
		}
	}
	sort.Slice(pairs, func(left, right int) bool {
		return pairs[left].key < pairs[right].key ||
			(pairs[left].key == pairs[right].key && pairs[left].value < pairs[right].value)
	})
	encoded := make([]string, len(pairs))
	for index := range pairs {
		encoded[index] = pairs[index].key + "=" + pairs[index].value
	}
	return strings.Join(encoded, "&")
}

func uriEncode(value string, encodeSlash bool) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	for index := 0; index < len(value); index++ {
		char := value[index]
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || strings.ContainsRune("-._~", rune(char)) ||
			(char == '/' && !encodeSlash) {
			encoded.WriteByte(char)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[char>>4])
		encoded.WriteByte(hexadecimal[char&15])
	}
	return encoded.String()
}

func credentialScope(now time.Time, region string) string {
	return now.Format(shortDateFormat) + "/" + region + "/" + service + "/" + requestType
}

func signature(secret string, now time.Time, region, stringToSign string) string {
	dateKey := hmacSHA256([]byte("AWS4"+secret), now.Format(shortDateFormat))
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, service)
	signingKey := hmacSHA256(serviceKey, requestType)
	return hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
}

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// CredentialSource finds the secret for an access key. Implementations must be
// safe for concurrent use.
type CredentialSource interface {
	Credential(ctx context.Context, accessKey string) (Credentials, error)
}

// CredentialSourceFunc adapts a function to CredentialSource.
type CredentialSourceFunc func(context.Context, string) (Credentials, error)

// Credential implements CredentialSource.
func (function CredentialSourceFunc) Credential(ctx context.Context, accessKey string) (Credentials, error) {
	return function(ctx, accessKey)
}

// V4Authenticator verifies SigV4 requests. ClockSkew defaults to five minutes;
// a negative value rejects all requests. Clock defaults to time.Now.
type V4Authenticator struct {
	Credentials CredentialSource
	Region      string
	Clock       func() time.Time
	ClockSkew   time.Duration
}

// Authenticate verifies request and returns its access key. It rejects
// header maps with multiple case variants of the same field.
func (authenticator V4Authenticator) Authenticate(request *http.Request, payload []byte) (string, error) {
	if request == nil || request.URL == nil || request.Method == "" || requestHost(request) == "" ||
		authenticator.Credentials == nil || authenticator.Region == "" ||
		strings.ContainsAny(authenticator.Region, "/, \t\r\n") || !validRequestURL(request) {
		return "", ErrAuthentication
	}
	headers, err := normalizedHeaders(request.Header)
	if err != nil {
		return "", ErrAuthentication
	}
	clone := *request
	clone.Header = headers
	request = &clone
	parsed, err := parseAuthorization(request.Header.Get("Authorization"))
	if err != nil {
		return "", err
	}
	date, err := time.Parse(amzDateFormat, request.Header.Get("x-amz-date"))
	if err != nil {
		return "", ErrAuthentication
	}
	clock := authenticator.Clock
	if clock == nil {
		clock = time.Now
	}
	now := clock().UTC()
	skew := authenticator.ClockSkew
	if skew == 0 {
		skew = 5 * time.Minute
	}
	if skew < 0 || date.Before(now.Add(-skew)) || date.After(now.Add(skew)) ||
		parsed.scope != credentialScope(date, authenticator.Region) {
		return "", ErrAuthentication
	}
	credentials, err := authenticator.Credentials.Credential(request.Context(), parsed.accessKey)
	if err != nil || !credentials.Valid() || credentials.AccessKey != parsed.accessKey {
		return "", ErrAuthentication
	}
	if !validSessionToken(request.Header.Get("x-amz-security-token"), credentials.SessionToken) {
		return "", ErrAuthentication
	}
	if request.Header.Get("x-amz-content-sha256") != sha256Hex(payload) {
		return "", ErrAuthentication
	}
	canonical, _, err := canonicalRequestForVerification(request, parsed.signedHeaders)
	if err != nil {
		return "", ErrAuthentication
	}
	stringToSign := algorithm + "\n" + date.Format(amzDateFormat) + "\n" + parsed.scope + "\n" + sha256Hex([]byte(canonical))
	want := signature(credentials.SecretKey, date, authenticator.Region, stringToSign)
	if !hmac.Equal([]byte(want), []byte(parsed.signature)) {
		return "", ErrAuthentication
	}
	return parsed.accessKey, nil
}

func validSessionToken(got, want string) bool { return hmac.Equal([]byte(got), []byte(want)) }

type authorization struct {
	accessKey     string
	scope         string
	signedHeaders string
	signature     string
}

func parseAuthorization(value string) (authorization, error) {
	if !strings.HasPrefix(value, algorithm+" ") {
		return authorization{}, ErrAuthentication
	}
	fields := strings.Split(strings.TrimPrefix(value, algorithm+" "), ",")
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
	}
	if len(fields) != 3 || !strings.HasPrefix(fields[0], "Credential=") ||
		!strings.HasPrefix(fields[1], "SignedHeaders=") || !strings.HasPrefix(fields[2], "Signature=") {
		return authorization{}, ErrAuthentication
	}
	credential := strings.TrimPrefix(fields[0], "Credential=")
	slash := strings.IndexByte(credential, '/')
	result := authorization{
		signedHeaders: strings.TrimPrefix(fields[1], "SignedHeaders="),
		signature:     strings.TrimPrefix(fields[2], "Signature="),
	}
	if slash <= 0 || slash == len(credential)-1 || result.signedHeaders == "" ||
		len(result.signature) != sha256.Size*2 {
		return authorization{}, ErrAuthentication
	}
	if _, err := hex.DecodeString(result.signature); err != nil {
		return authorization{}, ErrAuthentication
	}
	result.accessKey, result.scope = credential[:slash], credential[slash+1:]
	return result, nil
}

func canonicalRequestForVerification(request *http.Request, names string) (string, string, error) {
	list := strings.Split(names, ";")
	if len(list) == 0 || list[0] == "" || !sort.StringsAreSorted(list) {
		return "", "", ErrAuthentication
	}
	var headers strings.Builder
	seen := make(map[string]bool, len(list))
	for _, name := range list {
		if name != strings.ToLower(name) || seen[name] || (name != "host" && request.Header.Values(name) == nil) {
			return "", "", ErrAuthentication
		}
		seen[name] = true
		headers.WriteString(name)
		headers.WriteByte(':')
		if name == "host" {
			headers.WriteString(normalizeHeader(requestHost(request)))
		} else {
			for index, value := range request.Header.Values(name) {
				if index > 0 {
					headers.WriteByte(',')
				}
				headers.WriteString(normalizeHeader(value))
			}
		}
		headers.WriteByte('\n')
	}
	if !seen["host"] || !seen["x-amz-date"] || !seen["x-amz-content-sha256"] {
		return "", "", ErrAuthentication
	}
	for name := range request.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") && !seen[lower] {
			return "", "", ErrAuthentication
		}
	}
	payloadHash := request.Header.Get("x-amz-content-sha256")
	canonical := request.Method + "\n" + canonicalURI(request.URL.EscapedPath()) + "\n" +
		canonicalQuery(request.URL.Query()) + "\n" + headers.String() + "\n" + names + "\n" + payloadHash
	return canonical, names, nil
}
