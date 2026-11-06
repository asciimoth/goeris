// Package s3 stores encrypted ERIS blocks in the small subset of the Amazon
// S3 REST protocol described by the ERIS object-storage recommendation.
//
// The package supports public reads, authenticated single-request writes, and
// a fixed-bucket HTTP handler. Handler is read-only by default; set AllowPut
// to enable authenticated writes. It does not implement bucket administration,
// object listing, multipart upload, IAM, or credential discovery.
//
// Client and Handler do not create network clients or servers. Applications
// supply an HTTPClient, block store, credentials, and clocks as applicable.
package s3
