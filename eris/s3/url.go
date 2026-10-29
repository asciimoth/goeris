package s3

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/asciimoth/eris-tools/eris"
)

// StoreURL identifies one bucket at an HTTPS S3 endpoint. Its zero value is
// invalid.
type StoreURL struct {
	host   string
	bucket string
}

// ParseStoreURL parses a canonical s3://ENDPOINT/BUCKET/ URL. ENDPOINT can
// include a port. Bucket names are restricted to one non-empty path segment.
func ParseStoreURL(text string) (StoreURL, error) {
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return StoreURL{}, ErrInvalidStoreURL
	}
	if parsed.Hostname() == "" || invalidPort(parsed) {
		return StoreURL{}, ErrInvalidStoreURL
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasSuffix(path, "/") || strings.Count(path, "/") != 1 {
		return StoreURL{}, ErrInvalidStoreURL
	}
	bucket := strings.TrimSuffix(path, "/")
	if !validBucket(bucket) {
		return StoreURL{}, ErrInvalidStoreURL
	}
	store := StoreURL{host: parsed.Host, bucket: bucket}
	if store.String() != text {
		return StoreURL{}, ErrInvalidStoreURL
	}
	return store, nil
}

func invalidPort(parsed *url.URL) bool {
	port := parsed.Port()
	if port == "" {
		return strings.HasSuffix(parsed.Host, ":")
	}
	number, err := strconv.Atoi(port)
	return err != nil || number < 1 || number > 65535
}

func validBucket(bucket string) bool {
	if bucket == "" || bucket == "." || bucket == ".." {
		return false
	}
	for _, char := range bucket {
		if char <= ' ' || char >= 0x7f || char == '/' || char == '\\' {
			return false
		}
	}
	return true
}

// String returns the canonical store URL.
func (store StoreURL) String() string {
	if store.host == "" || !validBucket(store.bucket) {
		return ""
	}
	return "s3://" + store.host + "/" + store.bucket + "/"
}

// Host returns the HTTPS endpoint host, including its optional port.
func (store StoreURL) Host() string { return store.host }

// Bucket returns the bucket name.
func (store StoreURL) Bucket() string { return store.bucket }

// ObjectURL returns the path-style HTTPS URL for a canonical Base32 ERIS
// reference key.
func (store StoreURL) ObjectURL(key string) (*url.URL, error) {
	if store.String() == "" {
		return nil, ErrInvalidStoreURL
	}
	if _, err := eris.ParseReference(key); err != nil {
		return nil, ErrInvalidKey
	}
	return &url.URL{Scheme: "https", Host: store.host, Path: "/" + store.bucket + "/" + key}, nil
}
