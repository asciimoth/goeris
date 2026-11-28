package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	stdfs "io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/magnet"
	"github.com/asciimoth/zpgo"
	"golang.org/x/term"
)

const fsUsage = `Usage: goeris fs COMMAND [options] ARGUMENTS

  build [--archive] [--preserve-root] [--compression none|auto|lzip] [--secret-file FILE] SOURCE
      Print the new image URN. Supply a file or directory, or use --archive
      to read archive entries. A single file keeps its name. --preserve-root
      keeps the directory name or the archive name without its format suffix.
  pack IMAGE NEW-ARCHIVE        Write the image contents to a new archive.
  unpack IMAGE NEW-DIRECTORY    Extract the whole image into a new directory.
  ls IMAGE [PATH]               List names in one directory.
  tree IMAGE [PATH]             Show a directory tree. Skip link targets.
  stat IMAGE [PATH]             Show mode and size. Inspect the link itself.
  index IMAGE                   Show encoded root entries and storage properties.
  readlink IMAGE PATH           Print a link target checked for native use.
  cat IMAGE PATH                Stream one decoded file to stdout.
  decode IMAGE PATH [NEW-FILE]  Write one file to stdout or a new file.
  prefetch IMAGE [PATH]         Request content below a path (best effort).
  resolve IMAGE [PATH]          Print the host path when the store provides one.
  repl IMAGE                    Browse an open image. Enter help for commands.

IMAGE accepts a capability URN or an ERIS Magnet URI. On read commands,
--index-limit BYTES sets a positive decoded byte limit per index. Expanded
mounts use their own limits and cannot accept this option. Paths use / and start
at the image root. The default path is ".". Build uses no compression by
default. File modes preserve the executable flag.
Archive suffixes are .zip, .tar, .gz, .zlib, .deflate, and .lzw (LSB, 8 bits).
Compressed tar also accepts .tar before the compression suffix. The .tgz alias
selects gzip. The .zz alias selects zlib. Build also reads .bz2, .tar.bz2,
.tbz, and .tbz2. The full suffixes .gzip and .bzip2 also work.
` + storageHelp

type imageOptions struct {
	indexLimit                                  int64
	meter                                       *imageMeter
	op, source, dest, path, compression, secret string
	preserve                                    bool
	archive                                     imageArchive
	root                                        eris.Capability
}

// parseImageCommand validates the complete request before opening storage.
func parseImageCommand(flags *flag.FlagSet, args []string) (commandOptions, error) {
	c := commandOptions{name: "fs", usage: fsUsage}
	if err := flags.Parse(args); err != nil {
		return c, fmt.Errorf("%s: %w", c.name, err)
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		return c, flag.ErrHelp
	}
	o := &c.image
	o.op = args[0]
	c.name += " " + o.op
	var fromArchive bool
	minArgs, maxArgs := 1, 1
	switch o.op {
	case "build":
		flags.BoolVar(&fromArchive, "archive", false, "build from archive entries")
		flags.BoolVar(&o.preserve, "preserve-root", false, "keep the directory or archive name")
		flags.StringVar(&o.compression, "compression", "none", "none, auto, or lzip")
		flags.Func("secret-file", "32-byte convergence secret", func(value string) error {
			if value == "" {
				return errors.New("expected a path")
			}
			o.secret = value
			return nil
		})
	case "decode":
		minArgs, maxArgs = 2, 3
	case "cat", "readlink", "unpack", "pack":
		minArgs, maxArgs = 2, 2
	case "ls", "tree", "stat", "resolve", "prefetch":
		maxArgs = 2
	case "index", "repl":
	default:
		return c, fmt.Errorf("fs: unknown subcommand %q", o.op)
	}
	if o.op != "build" {
		flags.Func("index-limit", "decoded byte limit per index", func(value string) error {
			limit, err := strconv.ParseInt(value, 10, 64)
			if err != nil || limit <= 0 {
				return erisfs.ErrInvalidLimit
			}
			o.indexLimit = limit
			return nil
		})
	}
	if err := flags.Parse(args[1:]); err != nil {
		return c, fmt.Errorf("%s: %w", c.name, err)
	}
	args = flags.Args()
	if len(args) < minArgs || len(args) > maxArgs {
		return c, fmt.Errorf("%s: wrong number of arguments (see goeris fs --help)", c.name)
	}
	if o.op == "build" {
		switch o.compression {
		case "none", "auto", "lzip":
		default:
			return c, fmt.Errorf("%s: unknown compression %q", c.name, o.compression)
		}
		if args[0] == "" {
			return c, errors.New("fs build: empty source path")
		}
		o.source = args[0]
		if fromArchive {
			archive, err := parseImageArchive(o.source)
			if err != nil {
				return c, fmt.Errorf("%s: %w", c.name, err)
			}
			if o.preserve && (archive.stem == "." || !stdfs.ValidPath(archive.stem)) {
				return c, fmt.Errorf("%s: cannot preserve archive name %q", c.name, archive.stem)
			}
			o.archive = archive
		}
		return c, nil
	}
	var err error
	scheme, _, _ := strings.Cut(args[0], ":")
	if strings.EqualFold(scheme, magnet.Scheme) {
		var uri magnet.URI
		uri, err = magnet.Parse(args[0])
		o.root = uri.ExactTopic
	} else {
		o.root, err = eris.ParseURN(args[0])
	}
	if err != nil {
		return c, fmt.Errorf("%s: invalid image URI: %w", c.name, err)
	}
	o.path = "."
	if o.op == "unpack" || o.op == "pack" {
		if args[1] == "" {
			return c, fmt.Errorf("%s: empty destination", c.name)
		}
		o.dest = args[1]
		if o.op == "pack" {
			o.archive, err = parseImageArchive(o.dest)
			if err != nil {
				return c, fmt.Errorf("%s: %w", c.name, err)
			}
			if o.archive.format == "bz2" {
				return c, fmt.Errorf("%s: bzip2 archives are supported for input only", c.name)
			}
		}
	} else if len(args) > 1 {
		o.path = args[1]
		if !stdfs.ValidPath(o.path) {
			return c, fmt.Errorf("%s: invalid image path %q", c.name, o.path)
		}
	}
	if o.op == "decode" && len(args) == 3 {
		if args[2] == "" {
			return c, errors.New("fs decode: empty destination")
		}
		o.dest = args[2]
	}
	return c, nil
}

func (o imageOptions) run(p *zpgo.Progress, store erisfs.ImageStore, input *os.File) error {
	ctx := erisfs.WithReadDiagnostics(context.Background())
	if o.op == "repl" {
		active, finish, err := o.meter.replActivity(p, "open image")
		if err != nil {
			return err
		}
		view, err := o.open(ctx, store, active)
		finish()
		if err != nil {
			return err
		}
		b := imageBrowser{ctx: ctx, view: view, p: p, meter: o.meter, inREPL: true}
		return b.repl(input, term.IsTerminal(int(input.Fd())))
	}
	stop := o.meter.activity(p, o.op, false)
	defer stop()
	if o.op == "build" {
		root, err := o.build(ctx, store, p.Root())
		if err != nil {
			return err
		}
		_, err = p.PrintfStdout("%s\n", root)
		return err
	}
	if o.op == "index" {
		var index erisfs.Index
		var err error
		if o.indexLimit > 0 {
			index, err = store.DecodeIndexWithLimit(ctx, o.root, o.indexLimit)
		} else {
			index, err = store.DecodeIndex(ctx, o.root)
		}
		if err != nil {
			return err
		}
		return printImageIndex(p, index)
	}
	view, err := o.open(ctx, store, p)
	if err != nil {
		return err
	}
	b := imageBrowser{ctx: ctx, view: view, p: p, meter: o.meter}
	switch o.op {
	case "pack":
		return b.pack(o.dest, o.archive.format)
	case "unpack":
		return b.unpack(o.dest)
	case "decode":
		if o.dest == "" {
			return b.browse("cat", o.path, o.path)
		}
		return b.saveFile(o.path, o.dest)
	}
	return b.browse(o.op, o.path, o.path)
}

func (o imageOptions) open(ctx context.Context, store erisfs.ImageStore, p *zpgo.Progress) (erisfs.FileSystem, error) {
	opening := p.Root().Start("Opening image index", 0)
	defer opening.End()
	if o.indexLimit > 0 {
		return store.OpenFSWithLimit(ctx, o.root, o.indexLimit)
	}
	return store.OpenFS(ctx, o.root)
}

// printImageIndex reports stored properties without reading file contents.
func printImageIndex(p *zpgo.Progress, index erisfs.Index) error {
	var err error
	for _, e := range index.Entries {
		name := strings.Join(e.Path, "/")
		switch e.Type {
		case erisfs.Symlink:
			_, err = p.PrintfStdout("%q symlink target=%q\n", name, e.Target)
		case erisfs.Subdirectory:
			_, err = p.PrintfStdout("%q directory %s\n", name, e.Capability)
		default:
			compression := "none"
			if e.Compression == erisfs.Lzip {
				compression = "lzip"
			}
			size := "unknown"
			if e.Size >= 0 {
				size = strconv.FormatInt(e.Size, 10)
			}
			_, err = p.PrintfStdout("%q file size=%s executable=%t compression=%s %s\n", name, size, e.Type == erisfs.Executable, compression, e.Capability)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// imageBrowser shares an open image between commands in a REPL session.
type imageBrowser struct {
	ctx    context.Context
	view   erisfs.FileSystem
	p      *zpgo.Progress
	meter  *imageMeter
	inREPL bool // Use type codes and quote path results in the REPL.
}

// browse uses name for image lookups and label for display.
func (b imageBrowser) browse(op, name, label string) error {
	if !stdfs.ValidPath(name) {
		return fmt.Errorf("invalid image path %q", name)
	}
	if op == "cat" {
		return b.copyToStdout(name)
	}
	if op == "tree" {
		entry, err := b.treeEntry(name)
		if err != nil {
			return err
		}
		return b.printTree(name, label, entry)
	}
	node := b.p.Root().Start(op+" "+displayImagePath(label), 0)
	defer node.End()
	var value string
	var err error
	switch op {
	case "ls":
		entries, err := b.view.ReadDir(b.ctx, name)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := b.printListingEntry("", e.Name(), e); err != nil {
				return err
			}
			node.CompleteOne()
		}
		return nil
	case "stat":
		info, err := b.view.Lstat(b.ctx, name)
		if err != nil {
			return err
		}
		return b.printStat(info)
	case "readlink":
		value, err = b.view.ReadLink(b.ctx, name)
	case "resolve":
		value, err = b.view.ResolvePath(b.ctx, name)
	case "prefetch":
		return b.view.Prefetch(b.ctx, name)
	default:
		return fmt.Errorf("unknown command %q", op)
	}
	if err == nil {
		if b.inREPL {
			value = displayImagePath(value)
		}
		_, err = b.p.PrintfStdout("%s\n", value)
	}
	return err
}

func (b imageBrowser) printStat(info stdfs.FileInfo) error {
	_, err := b.p.PrintfStdout("mode=%s size=%d\n", info.Mode(), info.Size())
	return err
}

// Walk from the full image view so each directory can use fallback storage.
func (b imageBrowser) printTree(name, label string, entry stdfs.DirEntry) error {
	node := b.p.Root().Start("tree "+displayImagePath(label), 0)
	defer node.End()
	if err := b.printListingEntry("", label, entry); err != nil {
		return err
	}
	node.CompleteOne()
	if entry.IsDir() {
		return b.printTreeChildren(name, "", node)
	}
	return nil
}

// Read the selected entry from its parent, just like the children below it.
// Lstat needs to open a v1 file to find its size. Use it only for the image
// root or when the backend cannot list the requested entry.
func (b imageBrowser) treeEntry(name string) (stdfs.DirEntry, error) {
	if name != "." {
		entries, err := b.view.ReadDir(b.ctx, path.Dir(name))
		if err == nil {
			base := path.Base(name)
			for _, entry := range entries {
				if entry.Name() == base {
					return entry, nil
				}
			}
		}
	}
	info, err := b.view.Lstat(b.ctx, name)
	if err != nil {
		return nil, err
	}
	return stdfs.FileInfoToDirEntry(info), nil
}

// printTreeChildren keeps a vertical line under each ancestor with siblings
// still to print. Entry names are relative to their displayed parent.
func (b imageBrowser) printTreeChildren(dir, indent string, node zpgo.Node) error {
	entries, err := b.view.ReadDir(b.ctx, dir)
	if err != nil {
		return err
	}
	for i, entry := range entries {
		branch, below := "├── ", "│   "
		if i == len(entries)-1 {
			branch, below = "└── ", "    "
		}
		if err := b.printListingEntry(indent+branch, entry.Name(), entry); err != nil {
			return err
		}
		node.CompleteOne()
		if entry.IsDir() {
			if err := b.printTreeChildren(path.Join(dir, entry.Name()), indent+below, node); err != nil {
				return err
			}
		}
	}
	return nil
}

// printListingEntry uses the type bits from ReadDir. Only regular files
// need permission bits to distinguish executable files. Encoded entries
// expose Mode directly, even when Info would have to read a legacy file
// to determine its size.
func (b imageBrowser) printListingEntry(prefix, name string, entry stdfs.DirEntry) error {
	label := displayImagePath(name)
	if b.inREPL {
		mode := entry.Type()
		if info, ok := entry.(stdfs.FileInfo); ok {
			mode = info.Mode()
		} else if mode.IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return fmt.Errorf("read mode for %q: %w", name, err)
			}
			mode = info.Mode()
		}
		label = listingTypeCode(mode) + " " + label
	}
	_, err := b.p.PrintfStdout("%s%s\n", prefix, label)
	return err
}

func listingTypeCode(mode stdfs.FileMode) string {
	switch mode.Type() {
	case 0:
		if mode.Perm()&0111 != 0 {
			return "x"
		}
		return "f"
	case stdfs.ModeDir:
		return "d"
	case stdfs.ModeSymlink:
		return "l"
	case stdfs.ModeNamedPipe:
		return "p"
	case stdfs.ModeSocket:
		return "s"
	case stdfs.ModeDevice:
		return "b"
	case stdfs.ModeDevice | stdfs.ModeCharDevice:
		return "c"
	default:
		return "u"
	}
}
