// Package link encodes and decodes ERIS link files.
//
// A link file contains an ERIS read capability, the decoded content size, a
// media type, and optional metadata attributes. The package only operates on
// byte slices. It does not access a file system or network.
//
// Parsing checks the link structure. It does not retrieve the content or
// verify that Size, MIME, or Attributes describe that content. Treat these
// fields as untrusted metadata. Check Size against an application limit
// before using it to allocate memory. Validate MIME before using it in a
// protocol header, and validate attribute values before using them as paths
// or other application input.
//
// Parsers limit CBOR nesting but do not impose a byte limit. Limit input size
// before reading a link file into memory and before calling Parse,
// UnmarshalBinary, or ParseValue. Memory use grows with the input size and
// attribute count. HasHeader only checks the magic bytes; it is not validation.
//
// See the [ERIS link file recommendation].
//
// [ERIS link file recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/linkfile/index.md
package link
