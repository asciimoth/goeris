// Package link encodes and decodes ERIS link files.
//
// A link file contains an ERIS read capability, the decoded content size, a
// media type, and optional metadata attributes. The package only operates on
// byte slices. It does not access a file system or network.
//
// See the [ERIS link file recommendation].
//
// [ERIS link file recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/linkfile/index.md
package link
