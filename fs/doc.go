// Package fs writes version 2 of the [ERIS-FS recommendation] and reads
// versions 1 and 2. It also reads the version 1 text paths used by eris-go,
// and the text symlink targets and length-first map order used by earlier
// releases of this library.
//
// The low-level API contains Index, Entry, and the index codecs. It uses
// the block source and sink interfaces in the eris package so applications can
// control storage and transport. Raw content encoding and ERIS tree reading
// are provided by eris.EncodeContent, eris.EncodeContentRecommended, and
// eris.NewReader.
//
// The high-level API contains Builder, FileSystem, and OpenFS. It exposes an
// encoded image without extraction. All storage operations accept context.
// NewIOFS(ctx, image) binds a context and implements the standard io/fs
// interfaces. Open files and directory entries retain that context. OpenFS
// uses its context only to read the initial index; the returned FS does not
// retain it. Later FS calls accept their own contexts.
// OpenFile also gives typed access to seeking and random-access reads for
// uncompressed files. AddCompressedFile creates deterministic Lzip content;
// compressed files are read sequentially with Open. Both use ERIS reader
// read-ahead hints. FS.Prefetch explicitly requests a file or directory tree;
// it reads required indexes and sends best-effort hints for file leaves.
// AddAutoFile selects compression with the fixed ERIS-FS-AUTO-1 sample policy.
// ChooseCompression exposes the same decision without encoding a file.
//
// NewMount(ctx, source, options) exposes the [eris-go mount layout] through
// io/fs. Paths at C return decoded content; paths at fs/C expose an ERIS-FS
// image, where C is a full capability URN, unprefixed base32, or hexadecimal.
// By default, the root lists only fs, and fs lists no entries. Capabilities
// resolve on demand. NewMountWithLimit limits each decoded index. Mount keeps
// no image cache; Sub retains a selected image for repeated access. The caller
// can use Mount in a FUSE or 9P adapter. It does not mount a host filesystem.
// For a base32 block hash H, Mount also exposes encrypted block bytes at
// H[:3]/H[3:6]/H[6:9]/H[9:], matching volume.BlockPath, and at the flat path H.
// Prefix directories resolve on demand and have empty listings by default.
// Hashes, capabilities, and fs accept ASCII case variants; image entry paths keep
// their original case-sensitive lookup rules.
// Mount.WithListingCache enables bounded LRU listings for recently opened
// capabilities and verified raw blocks. Limits are separate for each category;
// zero disables a category. Only known image capabilities appear under fs.
// The cache holds identifiers and metadata, not content. Virtual Sub views
// share it, and open directories retain snapshots through later evictions.
//
// ResolveRoot and ResolvePath find absolute host paths on compatible ERIS-FS
// mounts. Supply the directory that contains capability trees, such as /eris/fs.
// Without an explicit path, they use ERIS_FS_MNT, then ERIS_MNT with /fs
// appended, selecting the first nonempty setting. If both are empty or unset, they
// search /n/eris/fs and /eris/fs in order. A configured mount limits the search
// to that mount. Both functions return "" when no matching path is found.
//
// # Deterministic encoding
//
// To match capabilities across implementations, use Builder.AddFile for every
// file, the same 32-byte convergence secret, and the same version 2 index
// structure, paths, executable flags, and symlink targets. All encoders must
// use bytewise deterministic CBOR and the recommended block sizes: 1 KiB below
// 16384 stored bytes, and 32 KiB at or above that size, including index bytes.
// The builder applies these CBOR and block-size rules. Use one root index with
// full relative paths, or agree on the same nested index boundaries.
//
// For repeatable encoding with automatic compression in goeris, use
// Builder.AddAutoFile for every file and keep the encoding profile fixed,
// including the goeris and effective LZMA dependency versions. The auto policy
// fixes the compression choice; matching compressed capabilities also requires
// matching Lzip bytes. An 8 MiB dictionary alone does not ensure this across
// different encoders. Entry insertion order and reader chunk sizes do not
// affect successful encoding. See the [deterministic encoding procedures] for
// complete settings and examples.
//
// # Input and decoding limits
//
// The image APIs operate on io.Reader values and abstract ERIS block stores.
// They do not access the host file system, network, clock, or random source.
// ResolveRoot and ResolvePath read environment variables and host file metadata.
// Encoding returns io.ErrNoProgress after 100 consecutive empty reads from
// an input. Temporary empty reads below this limit do not change the output.
// Encoding stops at the first io.EOF, including EOF returned with data.
//
// For untrusted images, DecodeIndex and OpenFS limit the encoded CBOR bytes
// of each index. This limit does not bound the memory used by the parsed tree
// or the total size of nested indexes. ParseIndex and UnmarshalBinary do not
// apply a size limit to their input slices. Applications must bound those
// inputs before parsing them.
//
// File sizes from version 2 indexes are untrusted metadata. Stat does not
// validate file content. Read through EOF to check the complete file, including
// compression checksums. ReadFile has no content size limit; use Open with a
// bounded reader when only a limited amount of output is needed. Block sources
// must verify that each returned block matches its requested ERIS reference,
// as required by eris.BlockSource.
//
// An output limit does not limit all input work. Opening a compressed file or
// finding a version 1 file's size scans its ERIS tree. A Lzip stream can also
// contain many empty members. Applications that need a total I/O budget or a
// deadline must enforce it in their block source and return an error when it
// is reached. MaxLzipDictionarySize limits each decoder, not total process
// memory or the number of open files.
//
// Image paths use io/fs rules. Backslashes and names that are special on a host
// operating system are not converted or filtered by the image APIs. ResolvePath
// converts io/fs names to native paths and rejects names that cannot be
// represented on the host. ReadLink rejects targets that can leave the image
// or contain a backslash, colon, or NUL byte. Applications that extract an
// image must still check native paths before writing them. Open follows links
// only inside the image, but Sub does not confine links to the selected
// subdirectory.
//
// [ERIS-FS recommendation]: https://github.com/asciimoth/goeris/blob/master/specs/eer/eris-fs/index.md
// [deterministic encoding procedures]: https://github.com/asciimoth/goeris/blob/master/specs/eris-fs-encoding.md
// [eris-go mount layout]: https://github.com/asciimoth/goeris/blob/master/specs/eris-go-fuse-layout.md
package fs

const (
	// SpecificationVersion is the ERIS-FS format version.
	SpecificationVersion = 2
	// LegacySpecificationVersion is the earlier format version that readers support.
	LegacySpecificationVersion = 1
	// LzipDictionarySize is the dictionary size used by this library's encoder.
	LzipDictionarySize = 8 << 20
	// LzipLengthTag marks an uncompressed file length for Lzip content.
	LzipLengthTag uint64 = 1280985424
	// CBORTag is the registered ERIS-FS CBOR tag.
	CBORTag uint64 = 1701996916
	// MagicPrefix is the five-byte CBOR tag prefix of an ERIS-FS index.
	MagicPrefix = "\xdaerit"
	// MIMEType is the media type for an ERIS-FS index.
	MIMEType = "application/x-eris-fs+cbor"
	// DefaultMaxIndexSize is the largest decoded index accepted by DecodeIndex
	// and OpenFS. Use the limit-specific functions when another bound is needed.
	DefaultMaxIndexSize int64 = 64 << 20
)
