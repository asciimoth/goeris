// Package fs writes version 2 of the [ERIS-FS recommendation] and reads
// versions 1 and 2. It also reads the version 1 text paths used by eris-go,
// and the text symlink targets and length-first map order used by earlier
// releases of this library.
//
// [NewStore] binds ERIS-FS to a [github.com/asciimoth/goeris.CapabilityStore].
// [Store.EncodeIndex] and [Store.DecodeIndex] write and read indexes.
// [Store.NewBuilder] builds an image from readers.
// [Store.OpenFS] opens an image by its root capability.
// It uses the supplied context to read the root index. Later image operations
// use the context supplied to each call. [NewIOFS] binds a context to an
// image for use through the standard io/fs interfaces. Open files and
// directory entries retain their operation context.
//
// [FS.OpenFile] supports seeking and random access for uncompressed files.
// [FS.ResolvePath] asks the backing store for the host path to an uncompressed
// file's content.
// [Builder.AddCompressedFile] writes deterministic Lzip content. Use [FS.Open]
// for sequential access to compressed files. [FS.Prefetch] reads the required
// indexes and sends a hint for each distinct file capability.
// [Builder.AddAutoFile] selects compression with the ERIS-FS-AUTO-1 policy.
// [ChooseCompression] exposes the same decision before encoding.
//
// [NewMountWithStore] exposes the [eris-go mount layout] through io/fs.
// Supply a *Store for decoded content at C, or any ImageStore for a tree at
// fs/C. C can be a full capability URN, unprefixed base32, or hexadecimal.
// The root lists fs when an image store is available. Capabilities resolve
// on demand. [NewMountWithStoreAndLimit] limits each decoded index.
// [Mount.Sub] retains a selected image for repeated access.
//
// Supply a block source to expose the encrypted block with base32 hash H at
// H and H[:3]/H[3:6]/H[6:9]/H[9:]. Prefix directories resolve on demand.
// Supply nil for either component to omit its paths. With both nil, the root
// directory is empty. Hashes, capabilities, and fs accept ASCII case variants.
// Paths within an image are case-sensitive.
//
// [Mount.WithListingCache] enables bounded listings of recently accessed
// capabilities and verified raw blocks. The two limits are independent.
// Zero disables a category. Only known image capabilities appear under fs.
// The cache holds identifiers and metadata. Virtual Sub views share this
// cache. An open directory retains its snapshot through later evictions.
//
// [ImageStore] supplies builders and read-only [FileSystem] views.
// [NewMountStore] opens decoded trees on a host mount. The view's ResolvePath method
// handles files and directories, including files decompressed by the mount.
// An empty mount directory uses ERIS_FS_MNT, then ERIS_MNT with /fs appended,
// then the default directories /n/eris/fs and /eris/fs.
// [NewCombinedStore] tries stores in order. Builders and open files retain
// their selected backend. [NewCombinedFileSystem] combines open image views.
// Prefetch visits every member of a combined view.
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
// The image APIs operate on io.Reader values and an ERIS capability store.
// The store controls content I/O.
// A MountStore reads host file metadata and content.
// Encoding returns io.ErrNoProgress after 100 consecutive empty reads from
// an input. Temporary empty reads below this limit do not change the output.
// Encoding stops at the first io.EOF, including EOF returned with data.
//
// For untrusted images, [Store.DecodeIndex] and [Store.OpenFS] limit the CBOR bytes
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
// finding a version 1 file's size can scan its ERIS tree on a block store.
// A Lzip stream can contain many empty members. Applications that need a total
// I/O budget or a deadline must enforce it in their store and return an error
// when it is reached. MaxLzipDictionarySize limits each decoder's dictionary.
//
// Image paths use io/fs rules. A capability-backed image treats backslashes
// and host-specific names literally. A mounted image converts io/fs names to
// native paths and rejects names the host cannot represent.
// ReadLink rejects targets that can leave the image
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
