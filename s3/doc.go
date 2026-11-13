// Package s3 stores encrypted ERIS blocks in the small subset of the Amazon
// S3 REST protocol described by the ERIS object-storage recommendation.
//
// See the [ERIS object-storage recommendation].
//
// The package supports public reads, authenticated single-request writes, and
// a fixed-bucket HTTP handler. Handler is read-only by default; set AllowPut
// to enable authenticated writes. It does not implement bucket administration,
// object listing, multipart upload, IAM, or credential discovery.
// SigV4 signing and verification reject HTTP trailers because their values are
// not covered by the supported signature format.
//
// Client and Handler do not create network clients or servers. Applications
// supply an HTTPClient, block store, credentials, and clocks as applicable.
// Applications must set HTTP timeouts and header limits. Block size limits do
// not limit the time spent waiting for a peer to send data.
// Handler checks cancellation before each storage call. Use context-aware
// block stores to cancel storage calls that are already in progress.
//
// [ERIS object-storage recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/object-storage/index.md
package s3

const (
	// ContentType is the media type for encrypted ERIS blocks.
	ContentType = "application/octet-stream"
	// PublicReadACL is the canned ACL for public block access.
	PublicReadACL = "public-read"
)
