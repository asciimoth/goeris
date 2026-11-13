// Package link encodes, decodes, and resolves ERIS link files.
//
// A link file contains an ERIS read capability, the decoded content size, a
// media type, and optional metadata attributes. The codec operates on byte
// slices. NewFS wraps an io/fs.FS and an eris.BlockSource to expose linked
// content at the original file paths. It uses only these supplied interfaces
// for file and block access.
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
// The FS wrapper limits each encoded link to DefaultMaxLinkSize by default.
// This does not limit resolved content. To bound content reads, use Open with
// io.LimitReader. A read that stops before the file end does not verify Size.
// Read one byte beyond your content limit and reject the result if it exceeds
// that limit. Otherwise, LimitReader can return a truncated file without an
// error. See the FS.Open example.
//
// Symbolic links use the base file system's policy. ReadLink and Lstat retain
// base support for inspecting them. Directory entry Info uses the same Lstat
// policy. These checks do not protect against concurrent path changes. Path
// validation does not prevent a base symbolic link from reaching outside its
// root. Use a confined base file system when paths or symbolic links come from
// an untrusted source.
//
// A read capability grants access to its content. Treat the complete link,
// its binary encoding, and formatted Link values as sensitive data. The codec
// returns fixed errors that do not include input data. FS methods and resolved
// file methods include the requested path in errors but hide unknown backend
// error text. The original cause remains accessible through errors.Is,
// errors.As, and unwrapping; it can contain private paths or credentials.
// Ordinary files returned directly from the base retain their error behavior.
// Do not expose paths, unwrapped errors, or partial ReadFile results to callers
// who lack access to them. ReadFile can return data together with an error.
//
// See the [ERIS link file recommendation].
//
// [ERIS link file recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/linkfile/index.md
package link
