# ERIS file-system volume v1

This document defines a local file-system format for encrypted ERIS blocks. It
is an original specification under the repository's CC0 license. It uses the
block references from [ERIS 1.0.0](eris-v1.0.0.html) and accepts the flat names
from the [object-storage recommendation](eer/object-storage/index.md).

The words MUST, MUST NOT, SHOULD, and MAY specify requirements, prohibitions,
recommendations, and options.

## Root path

A writable volume is one existing directory and its contents. The caller MUST
create the root directory before opening the volume. Opening a volume MUST NOT
create the root or scan its contents. Readers MUST also accept a regular file
root as an ERIS CBOR container, as specified below. Writers MUST reject a root
that is not a directory. Other file types are not supported and MUST be rejected
before opening them, so an existing FIFO cannot block volume construction.

No marker, index, configuration file, or lock file is required. A volume MAY
contain unrelated files and directories, including `README.md`. Readers and
writers MUST leave unrelated entries in place. They MUST NOT search arbitrary
paths for blocks or interpret unrelated files as volume configuration.

## Block names and paths

Each block file contains exactly one encrypted ERIS block, with no header,
padding added by the volume, compression, or other metadata. Its length MUST be
1,024 or 32,768 bytes. Both block sizes use the same directory tree.

Let `H` be the uppercase, unpadded RFC 4648 Base32 encoding of the block's
32-byte BLAKE2b-256 reference. `H` has exactly 52 characters. Its alphabet is
`A` through `Z` and `2` through `7`. Unused bits in the final character MUST be
zero; the final character is therefore `A` or `Q`. Lowercase, padded, and
noncanonical encodings are not block names in this format.

The canonical relative path MUST be:

```text
H[0:3]/H[3:6]/H[6:9]/H[9:52]
```

The ranges use zero-based offsets and exclude the end offset. There are exactly
three directory levels, each with three characters. The file name contains the
remaining 43 characters. Joining the four components recovers the complete
reference. `/` denotes a directory separator; implementations use the host
separator when needed.

The rule is fixed. It MUST NOT depend on the block size, directory entry count,
insertion order, or implementation settings. Each prefix level has at most
32,768 possible block subdirectories. Unrelated entries do not count toward this
bound. This format does not set a maximum number of files in a leaf directory.

The fixed names are subject to host restrictions. In particular, a prefix can be
`CON`, `PRN`, `AUX`, or `NUL`, which `os.Root` rejects on Windows. Such blocks
cannot use a directory volume on Windows, including flat fallback when the
canonical path is rejected. The package returns the path error; it does not
change the format or bypass `os.Root`. A CBOR file root can supply these blocks.

Examples:

```text
FGI/BBZ/22V/4GRMJVXNCSLVQHRP3NAPHRR5NW3GDPPXRJKBI452YBA
RY2/DQN/YEP/JD5TOJZME7MEK3RFFF75ZOIIFAJTKHP4SEMI3CPJXAQ
EHQ/2P3/JZU/X6TP3PZKFPMWOK4SBQKOEWKU52YJJQTKQJ66WWEQX4Q
CMC/7JN/IYG/VRPDTU4M2OUPKFNEEUPXYDWUNSINMAOBJXYUZA32CBA
```

## Read procedure

For a directory root and a requested reference, a reader MUST:

1. Try the canonical path.
1. If that path is absent, try the flat path `H` directly under the root.
1. If both paths are absent, report a missing block.
1. Before returning a block, verify its size and BLAKE2b-256 reference.

An absent parent directory counts as an absent canonical path. A permission
error, a non-directory parent, a non-regular block file, or invalid block data
is an error, not an absent path. Such errors MUST NOT cause flat fallback. If
both forms exist, the canonical form takes precedence. A corrupt canonical file
MUST produce an error even when the flat file is valid.

Readers MUST NOT return partial or unverified blocks. They SHOULD bound their
reads to 32,769 bytes, which is enough to detect an oversized file. Reads MUST
NOT create directories, migrate flat files, or change either block file.

This fallback supports existing flat stores. It does not make new nested blocks
readable by implementations that support only flat stores. Other directory
splits are not supported.

## Read-only ERIS CBOR roots

When the root path is a regular file, a reader MUST try to decode that file as
[ERIS CBOR](eer/cbor/index.md). Detection MUST NOT depend on the file extension.
This fallback applies to the root itself. Readers MUST NOT search for CBOR
containers inside a directory root.

The reader MUST accept the array and sequence forms supported by the `cbor`
package: tagged and untagged arrays, self-described tagged arrays, and tagged
and untagged sequences. Definite and indefinite lengths follow that package's
decoder rules. An empty file is a valid empty untagged sequence.

The reader MUST parse and validate the complete container before returning a
block. Invalid encoding, a truncated element, or an invalid reference-to-block
map MUST cause a decode error, including when a matching block occurs earlier in
the file. A raw encrypted block without CBOR encoding is not a valid container.

For each element, in file order:

1. For a standalone block, compute its reference and compare it with the
   requested reference.
1. For a block map, look up the requested reference. The decoder MUST verify the
   reference and size of every map entry.
1. References and read capabilities do not supply block bytes. Ignore them for
   lookup; do not follow them to another store.

Return the first matching block. Repeated valid blocks across elements are
allowed. If the valid container contains no match, report a missing block. All
returned blocks MUST have a valid ERIS size and match the requested hash.

File roots are read-only. Opening and reading MUST NOT change their bytes. Every
write attempt to an open file root MUST report that the root is not a directory,
even when the file is writable or contains invalid CBOR. A writer MUST NOT
replace the file, append to it, or create adjacent directories.

The Go package opens the file read-only and keeps its handle until `Close`. Each
read uses an independent offset, reads up to the file size observed at the
start, and validates the complete container before returning a block. Concurrent
reads are supported. There is no retained block cache or index. The reader
accepts the same formats as `cbor.Unmarshal` and `cbor.UnmarshalSequence`, but
reads in small parts. Byte strings are limited to 32,768 bytes before
allocation; map keys are limited to 32 bytes and capabilities to 66 bytes. These
limits also apply to the total length of indefinite byte strings. The reader
retains only the matching block, the current block, and the references needed to
detect duplicate keys in the current map. Memory use grows with the number of
entries in the largest map, rather than the total container size. Each lookup
takes time proportional to the container size; there is no fixed container-size
limit. Use a context deadline to limit parsing time for untrusted containers.
Applications must still allow enough memory for the reference set of each map
and for concurrent lookups.

Applications must keep the opened file unchanged during reads. A read does not
provide a snapshot against external writes. Replacing the path does not switch
an existing handle to the new file; reopen the volume to use it. Cancellation is
checked during parsing, including between elements, map entries, and byte-string
chunks, and before each underlying read. An active file-system call cannot be
interrupted by cancellation.

## Volume chains

A volume chain is an ordered list of volumes. Its text form is a list of
file-system paths separated by the literal character `;`. For example:

```text
/var/eris;/usr/backup.cbor;/home/user/.cache/eris
```

Applications SHOULD use the environment variable `ERIS_BLOCK_PATH` for this
list. `auto.Open()` uses `volume.OpenAvailableChain` to open its block store.
This helper skips missing volumes and tries to create selected platform default
directories. It skips a default volume if creation fails. These rules also apply
to default paths named in `ERIS_BLOCK_PATH`.

Applications that use strict volume chain construction can call
`volume.OpenChain(os.Getenv("ERIS_BLOCK_PATH"))`. An unset or empty variable
selects the default chain described below.

New volumes SHOULD be added to the **end** of the chain. Unlike the common
practice of prepending entries to `$PATH`, appending keeps the existing volumes
first for both reads and writes. A new volume supplies an additional fallback.
For example, when the variable is already nonempty:

```sh
export ERIS_BLOCK_PATH="$ERIS_BLOCK_PATH;/new/volume"
```

The path-list syntax is fixed:

- Split on literal `;` on every platform; do not use a platform-specific list
  separator.
- An empty string selects the platform's default chain.
- Reject leading, trailing, or consecutive separators. An empty entry MUST NOT
  imply the current directory. Use `.` explicitly if needed.
- Preserve entry order and duplicates. Do not trim whitespace or expand `~`,
  environment variables, or wildcard characters. Shell expansion, if desired, is
  the caller's responsibility.
- Relative paths refer to the working directory at construction time.
- There is no quoting or escape syntax within the list. Paths containing `;`
  cannot be represented. Colons are part of paths, so Windows drive-letter paths
  such as `C:\eris` can be used.

Strict construction with `OpenChain` MUST open every entry in order. If one
cannot be opened, it MUST close the handles already opened and return an error
without returning a partial chain. Missing roots are not silently skipped or
created. As with `Open`, a regular file is parsed as ERIS CBOR only when a read
is attempted.

### Default chains

When the chain string is empty, implementations MUST use these paths in order:

| Platform                                      | First volume               | Second volume                                              |
| --------------------------------------------- | -------------------------- | ---------------------------------------------------------- |
| Linux and other Unix systems, including macOS | `/eris/block`              | `$XDG_CACHE_HOME/eris/block`, or `$HOME/.cache/eris/block` |
| Windows                                       | `%ProgramData%\eris\block` | `%LOCALAPPDATA%\eris\block`                                |

On Unix, use `XDG_CACHE_HOME` only when it is nonempty and absolute. Otherwise,
use `$HOME/.cache`, as specified by the
[XDG Base Directory Specification](https://specifications.freedesktop.org/basedir/).
In that case, `HOME` MUST be nonempty and absolute; otherwise, report an error.
The Go implementation uses this rule on all platforms other than Windows.

On Windows, `ProgramData` and `LOCALAPPDATA` MUST both be nonempty absolute
paths; otherwise, report an error. These variables select the shared application
data directory and the user's local application data directory, respectively.
Their usual locations are `C:\ProgramData` and `C:\Users\<user>\AppData\Local`;
see
[Windows known folders](https://learn.microsoft.com/en-us/windows/win32/shell/knownfolderid).
Do not assume that the system drive is `C:`.

Resolve these environment variables each time a default chain is requested.
Append `eris/block` using the host directory separator. Treat each resulting
path as one entry, even if an environment value contains `;`. This resolution
applies only to default paths; explicit path lists still use literal paths.

For `OpenChain`, default roots follow the same rules as explicit roots: they
MUST already exist, and construction MUST fail if any root cannot be opened. Do
not create or skip missing default roots. Applications can use
`volume.DefaultChainPaths()` to obtain the paths before preparing the roots.
This function does not open or create them. A nonempty chain string replaces the
default chain completely.

### Chain operations

Operations MUST follow the root package's `eris.CombinedBlockStore` behavior:

1. Try each volume from left to right until one succeeds. A read returns the
   first verified block. A write stops at the first successful write and does
   not copy the block to later volumes.
1. Continue after any entry error, including missing blocks, corrupt data,
   invalid CBOR, or write failures. A file root returns `ErrNotDirectory` on
   writes, so a later directory can accept the block.
1. If all entries fail, return the last error. An empty chain returns
   `eris.ErrNoBlockStorage`.

The chain does not migrate blocks, copy successful reads into earlier volumes,
or repair a failed earlier volume. A read failure inside one volume can thus
fall back to the next volume, even though that volume's own flat-path fallback
is limited to absent nested paths. A write that reports a directory-sync error
may already have published its block; a later successful attempt can leave
copies in more than one volume.

The optional `Chain.OnError` callback receives discarded errors with zero-based
entry indexes. It does not receive the final error returned to the caller.
Context handling and TTL pass-through are unchanged from `CombinedBlockStore`.
An already canceled context takes priority over an empty-chain error.

`Chain.Close` closes all owned volume handles and joins close errors. The caller
MUST finish all operations before closing the chain. The zero `Chain` is empty;
closing it succeeds. The caller MUST NOT replace its embedded combined store.

## Write procedure

A writer MUST first require a directory root, then:

1. Check the block size and compute its reference. If an expected reference was
   supplied, check it before changing the volume.
1. Create missing prefix directories on demand. Do not create the volume root.
1. Write the complete block to a new temporary file in the destination leaf
   directory. Create the file exclusively, with a name that cannot be a block
   name.
1. Flush and close the temporary file. Rename it to the canonical file name. Use
   a file system with atomic replacement rename when atomic visibility is
   required.
1. Where directory sync is supported, sync the leaf directory, then each prefix
   parent, then the volume root. Finish these syncs before reporting success.
1. Remove the operation's temporary file on failure when possible.

Writers MUST NOT write blocks to flat paths. An existing flat copy MUST remain
unchanged, including when a canonical copy is written. A writer MAY replace an
existing canonical block file. This also permits repair of a corrupt canonical
file. A directory that occupies the block file path, or a non-directory entry
that occupies a required prefix path, MUST cause an error and remain in place.

Concurrent writers use distinct temporary files. On file systems with atomic
replacement rename, a reader sees either the previous complete file or the new
complete file, never the temporary write. Concurrent writes of the same valid
block have the same final bytes. These guarantees assume that other writers also
follow this procedure.

Failed writes MAY leave empty prefix directories. A process crash MAY leave
temporary files. Readers MUST ignore these files. Writers MUST NOT remove
temporary files owned by other operations. This format does not define automatic
cleanup, deletion, listing, or garbage collection.

## Interrupted writes and durability

The temporary-file procedure follows the same principle as
[Git's loose-object publication](https://github.com/git/git/blob/master/object-file.c).
Git can publish with a hard link or rename, and provides configurable
[file-sync protection](https://git-scm.com/docs/git-config#Documentation/git-config.txt-corefsync).
This volume uses replacement rename so a write can also repair a corrupt block.
It MUST NOT fall back to copying bytes into the final file or deleting the final
file before rename when rename fails.

On a file system with atomic replacement rename:

- If the writer stops before rename, the final path is unchanged. A partial
  temporary file can remain, but readers ignore it.
- If the writer stops during rename, the final path has either the old state
  (possibly absent) or the complete new block.
- If the writer stops after rename, the complete block is visible. The caller
  might not have received a success result.

A system crash or power loss also involves storage caches. Sync the file before
rename to persist its bytes. Then sync directories to persist their entries;
[file sync alone does not ensure directory-entry persistence](https://man7.org/linux/man-pages/man2/fsync.2.html).
Sync all three prefix directories and the root on every write, including when
they already exist. Another concurrent writer might have just created them. The
caller is responsible for making the pre-created root and its ancestors durable
before use.

The Go package attempts directory sync on Unix. It skips a directory sync only
when the sync call returns `EINVAL`, `ENOTSUP`, or `ENOSYS`. Other errors,
including I/O and permission errors, are returned. On other platforms, the
package skips directory sync. File sync remains required on all platforms; a
file-sync failure prevents rename.

A directory-sync failure can occur after publication. In this case, the write
returns an error but the complete block can already be visible. The writer MUST
NOT remove the published block to roll back the write. Retrying the same block
is safe. Cancellation after the final pre-rename check does not stop directory
sync.

Directory sync requires read access to the directories on Unix. It adds up to
four directory syncs per block, in addition to the file sync. These operations
favor durability over write throughput. Guarantees still depend on the operating
system, file system, and storage device honoring sync and rename. Unsupported
directory sync, volatile storage, and independently changed symbolic-link or
mount layouts do not provide the same durability. These steps do not promise
power-loss durability on all platforms. The package does not emulate atomic
rename on platforms where it is unavailable.

## Go package

`github.com/asciimoth/goeris/volume` provides:

- `Open(path) (*Volume, error)` to open an existing directory or read-only file
  root; CBOR parsing occurs on reads, not on open;
- `OpenChain(pathList) (*Chain, error)` to open a semicolon-separated list and
  combine it with `eris.CombinedBlockStore`, using default paths for an empty
  string;
- `OpenAvailableChain(pathList) (*Chain, error)` to prepare selected default
  directories and open the available volumes for application setup;
- `Chain.Len()` to count the volumes in a chain;
- `DefaultChainPaths() ([]string, error)` to resolve the platform's default
  paths without opening or creating roots;
- `BlockPath(ref) string` to compute the canonical relative host path;
- `GetBlock(ctx, requests)`, `PutBlock(ctx, requests)`,
  `UpdateBlockOptions(ctx, requests)`, and `BatchSize()` to implement
  `eris.BlockStore`; and
- `Close()` to release the root handle.

Volumes report a batch limit of one. Empty batches succeed without I/O. Larger
batches return `eris.ErrBatchTooLarge` before processing. Each write request can
include an expected reference. Invalid size or a reference mismatch produces an
error that matches `eris.ErrInvalidBlock`, without writing a block. Invalid
sizes also match `eris.ErrInvalidBlockSize`. Invalid stored data matches
`eris.ErrInvalidBlock`. Missing blocks match both `eris.ErrMissingBlock` and
`fs.ErrNotExist`. Other file-system errors retain their underlying error values
for `errors.Is`. File-root writes match `volume.ErrNotDirectory`. Invalid CBOR
containers match `cbor.ErrInvalidEncoding`. Unsupported root file types match
`fs.ErrInvalid`. A canceled call or closed handle returns its context or
file-system error before checking the root type. Empty entries in a nonempty
chain path list match `volume.ErrInvalidChain`. Chain open errors identify the
entry index and preserve the underlying error. Missing or invalid environment
values required to resolve default paths match `fs.ErrInvalid`.

The package creates directories with mode `0755` and files with mode `0644`,
subject to the process umask. The caller controls root access permissions. The
temporary file prefix is `.eris-tmp-`. A replacement file uses the new file
mode; it does not preserve the old file's metadata.

Directory operations use `os.Root`. Relative symbolic links within the root can
be followed; absolute links and links that escape the root are rejected by
`os.Root`. Writers replace a final block-path symbolic link rather than write
through it. The root path itself follows `os.OpenRoot` semantics. The package
inherits the platform limits of `os.Root`; it does not protect against hostile
mount changes or all effects of special-file replacement during an operation. On
Unix, file opens use nonblocking mode and check the opened file type before
reading, so a FIFO replacement cannot make an open wait for a writer. Directory
opens require a directory, including after path replacement. These checks do not
make a directory writable by an attacker safe: an attacker can still remove or
replace entries. Applications must control who can change the directory tree. A
file root follows symbolic links in the supplied root path.

Concurrent block operations are supported, including separate volume handles and
processes. The caller MUST finish operations before closing their handle. A nil
or zero-value `Volume` returns `fs.ErrClosed`. Methods on a closed handle return
file-system errors.

Block methods check cancellation between file operations. They cannot interrupt
a file-system call already in progress. Cancellation detected before the final
rename prevents publication, but can leave empty directories. After the final
cancellation check, a write can succeed despite cancellation. A non-nil context
is required.

A volume is terminal storage. It ignores TTL, does not forward requests, and
does not expire blocks.
