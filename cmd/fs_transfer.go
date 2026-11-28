package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/internal/streamio"
	"github.com/asciimoth/zpgo"
)

// build keeps paths in a single index so that relative links can cross
// sibling directories. Only empty directories need their own child index.
func (o imageOptions) build(ctx context.Context, store erisfs.ImageStore, node zpgo.Node) (eris.Capability, error) {
	var zero eris.Capability
	secret, err := readSecret(o.secret)
	if err != nil {
		return zero, fmt.Errorf("read convergence secret: %w", err)
	}
	info, err := os.Lstat(o.source)
	if err != nil {
		return zero, err
	}
	// Pin the source directory. Concurrent changes to a child link cannot
	// redirect reads outside the selected host tree.
	hostDir, name := filepath.Split(o.source)
	base := name
	if o.archive.format != "" {
		if !info.Mode().IsRegular() {
			return zero, fmt.Errorf("expected a regular archive file at %q", o.source)
		}
		base = o.archive.stem
	} else if info.IsDir() {
		hostDir, name = o.source, "."
		if o.preserve {
			base, err = sourceDirectoryName(o.source)
			if err != nil {
				return zero, err
			}
		}
	}
	if hostDir == "" {
		hostDir = "."
	}
	host, err := os.OpenRoot(hostDir)
	if err != nil {
		return zero, err
	}
	defer func() { _ = host.Close() }()
	if info.IsDir() {
		opened, err := host.Stat(".")
		if err != nil {
			return zero, err
		}
		if !os.SameFile(info, opened) {
			return zero, fmt.Errorf("source directory %q changed before it could be opened", o.source)
		}
	}
	b, err := store.NewBuilder(secret)
	if err != nil {
		return zero, err
	}
	tree := imageBuild{ctx: ctx, host: host, store: store, builder: b, secret: secret, compression: o.compression, node: node}
	if o.archive.format != "" {
		err = tree.readArchive(name, info, o.archive.format)
	} else {
		err = tree.visit(name, info.Mode())
	}
	if err != nil {
		return zero, err
	}
	finishing := node.Start("Writing image index", 0)
	defer finishing.End()
	root, err := b.Finish(ctx)
	wrap := o.preserve && (info.IsDir() || o.archive.format != "")
	if err != nil || !wrap {
		return root, err
	}
	wrapper, err := store.NewBuilder(secret)
	if err != nil {
		return zero, err
	}
	if err := wrapper.AddSubdirectory(base, root); err != nil {
		return zero, err
	}
	return wrapper.Finish(ctx)
}

func trimHostPathSeparators(name string) string {
	return strings.TrimRightFunc(name, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	})
}

func sourceDirectoryName(source string) (string, error) {
	_, base := filepath.Split(trimHostPathSeparators(source))
	if base == "" || base == "." || base == ".." {
		// Resolve links before interpreting parent steps in the source path.
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", err
		}
		absolute, err := filepath.Abs(resolved)
		if err != nil {
			return "", err
		}
		_, base = filepath.Split(trimHostPathSeparators(absolute))
	}
	if base == "." || !stdfs.ValidPath(base) {
		return "", fmt.Errorf("cannot preserve directory name %q", base)
	}
	return base, nil
}

// imageBuild keeps one builder for the tree and reuses the same empty
// index capability for all empty directories.
type imageBuild struct {
	host        *os.Root
	ctx         context.Context
	store       erisfs.ImageStore
	builder     erisfs.ImageBuilder
	secret      eris.Secret
	empty       eris.Capability
	compression string
	node        zpgo.Node
}

func (tree *imageBuild) visit(name string, mode stdfs.FileMode) error {
	switch {
	case mode.IsDir():
		entries, err := stdfs.ReadDir(tree.host.FS(), name)
		if err != nil {
			return err
		}
		if len(entries) == 0 && name != "." {
			return tree.addEmptyDirectory(name)
		}
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if err := tree.visit(path.Join(name, entry.Name()), info.Mode()); err != nil {
				return err
			}
		}
		return nil
	case mode&os.ModeSymlink != 0:
		target, err := tree.host.Readlink(filepath.FromSlash(name))
		if err != nil {
			return err
		}
		return tree.builder.AddSymlink(name, filepath.ToSlash(target))
	case mode.IsRegular():
		return tree.file(name)
	default:
		return fmt.Errorf("%s: unsupported file mode %s", name, mode)
	}
}

func (tree *imageBuild) addEmptyDirectory(name string) error {
	if tree.empty == (eris.Capability{}) {
		eb, err := tree.store.NewBuilder(tree.secret)
		if err != nil {
			return err
		}
		tree.empty, err = eb.Finish(tree.ctx)
		if err != nil {
			return err
		}
	}
	return tree.builder.AddSubdirectory(name, tree.empty)
}

func (tree *imageBuild) file(name string) error {
	f, err := tree.host.OpenFile(filepath.FromSlash(name), streamio.ReadFileFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: expected a regular file", name)
	}
	return tree.addFile(name, info.Mode(), info.Size(), f)
}

func (tree *imageBuild) addFile(name string, mode stdfs.FileMode, size int64, r io.Reader) error {
	task := tree.node.Start("Encoding KiB: "+displayImagePath(name), kib(size))
	defer task.End()
	add := tree.builder.AddFile
	switch tree.compression {
	case "auto":
		add = tree.builder.AddAutoFile
	case "lzip":
		add = tree.builder.AddCompressedFile
	}
	if err := add(tree.ctx, name, &imageReader{Reader: r, node: task}, mode.Perm()&0111 != 0); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// transferFile gets the contents and mode from the same open file. Sequential
// reads support compression and let the decoder verify the stream at EOF.
func (b imageBrowser) transferFile(name string, write func(stdfs.FileInfo, io.Reader) error) (err error) {
	task := b.p.Root().Start("Reading KiB: "+displayImagePath(name), 0)
	defer task.End()
	f, err := b.view.Open(b.ctx, name)
	if err != nil {
		return fmt.Errorf("open image file %q: %w", name, err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q: expected a regular file", name)
	}
	task.SetEstimatedTotalItems(kib(info.Size()))
	if err := write(info, &imageReader{Reader: f, node: task}); err != nil {
		return fmt.Errorf("copy image file %q: %w", name, err)
	}
	return nil
}

func (b imageBrowser) copyToStdout(name string) error {
	return b.transferFile(name, func(_ stdfs.FileInfo, r io.Reader) error {
		_, err := io.Copy(stdoutWriter{b.p}, r)
		return err
	})
}

// writeNewFile uses exclusive creation. Close errors and incomplete reads
// both invalidate the file, so remove it before returning the error.
func (b imageBrowser) writeNewFile(root *os.Root, name, local string) (stdfs.FileInfo, error) {
	var created stdfs.FileInfo
	err := b.transferFile(name, func(info stdfs.FileInfo, r io.Reader) error {
		perm := stdfs.FileMode(0666)
		if info.Mode()&0111 != 0 {
			perm = 0777
		}
		f, err := root.OpenFile(local, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err != nil {
			return err
		}
		created, err = f.Stat()
		if err != nil {
			return errors.Join(err, f.Close())
		}
		_, err = io.Copy(f, r)
		return errors.Join(err, f.Close())
	})
	if err != nil && created != nil {
		err = errors.Join(err, removeImageOutput(root, local, created))
	}
	return created, err
}

// Check the identity before removing an output that might have moved.
func removeImageOutput(parent *os.Root, name string, created stdfs.FileInfo) error {
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.SameFile(created, info) {
		return parent.Remove(name)
	}
	return nil
}

func verifyImageOutput(dest string, created stdfs.FileInfo) error {
	info, err := os.Lstat(dest)
	if err != nil {
		return fmt.Errorf("verify output %q: %w", dest, err)
	}
	if !os.SameFile(created, info) {
		return fmt.Errorf("output %q changed during the transfer", dest)
	}
	return nil
}

func (b imageBrowser) saveFile(name, dest string) error {
	dir, local, err := openImageDestination(dest, "file")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	created, err := b.writeNewFile(dir, name, local)
	if err != nil {
		return err
	}
	if err := verifyImageOutput(dest, created); err != nil {
		return errors.Join(err, removeImageOutput(dir, local, created))
	}
	return nil
}

// Keep the parent path as supplied so the host resolves links and parent steps.
func openImageDestination(dest, kind string) (*os.Root, string, error) {
	parent, local := filepath.Split(dest)
	if local == "" || local == "." || local == ".." {
		return nil, "", fmt.Errorf("%q: expected a new %s path", dest, kind)
	}
	if parent == "" {
		parent = "."
	}
	dir, err := os.OpenRoot(parent)
	return dir, local, err
}

type imageLink struct{ name, target string }

type imageOutput struct {
	name string
	info stdfs.FileInfo
}

// unpack owns a new directory. Exclusive creation also detects names that
// collide on a case-insensitive host. ReadLink validates each exported target.
func (b imageBrowser) unpack(dest string) (err error) {
	dest = trimHostPathSeparators(dest)
	parent, local, err := openImageDestination(dest, "directory")
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	if err := parent.Mkdir(local, 0700); err != nil {
		return err
	}
	created, err := parent.Lstat(local)
	if err != nil {
		return err
	}
	if !created.IsDir() {
		return fmt.Errorf("unpack destination %q changed before it could be opened", dest)
	}
	defer func() {
		if err != nil {
			// Remove only an empty directory here. The tree cleanup runs
			// inside the open root, even if the destination path moves.
			err = errors.Join(err, removeImageOutput(parent, local, created))
		}
	}()
	root, err := parent.OpenRoot(local)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	destination, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(created, destination) {
		return fmt.Errorf("unpack destination %q changed before it could be opened", dest)
	}
	var outputs []imageOutput
	defer func() {
		if err == nil {
			return
		}
		// Remove children before their parents. Other entries in the
		// destination can belong to a concurrent writer.
		for _, output := range slices.Backward(outputs) {
			if cleanupErr := removeImageOutput(root, output.name, output.info); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()
	recordOutput := func(local string) error {
		info, err := root.Lstat(local)
		if err == nil {
			outputs = append(outputs, imageOutput{local, info})
		}
		return err
	}
	var links []imageLink
	err = stdfs.WalkDir(erisfs.NewIOFS(b.ctx, b.view), ".", func(name string, d stdfs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		local, err := filepath.Localize(name)
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			// A writable mount can expose the directory we just created.
			// Stop before WalkDir starts copying the output into itself.
			info, err := d.Info()
			if err != nil {
				return err
			}
			if os.SameFile(destination, info) {
				return fmt.Errorf("unpack destination is inside the image at %q", name)
			}
			if err := root.Mkdir(local, 0755); err != nil {
				return err
			}
			return recordOutput(local)
		case d.Type()&os.ModeSymlink != 0:
			target, err := b.view.ReadLink(b.ctx, name)
			if err != nil {
				return err
			}
			links = append(links, imageLink{local, filepath.FromSlash(target)})
			return nil
		case d.Type().IsRegular():
			info, err := b.writeNewFile(root, name, local)
			if err == nil {
				outputs = append(outputs, imageOutput{local, info})
			}
			return err
		default:
			return fmt.Errorf("%s: unsupported file type %s", name, d.Type())
		}
	})
	if err != nil {
		return err
	}
	err = restoreImageLinks(root, links, func(link imageLink) error {
		if err := root.Symlink(link.target, link.name); err != nil {
			return err
		}
		return recordOutput(link.name)
	})
	if err != nil {
		return err
	}
	return verifyImageOutput(dest, destination)
}

func restoreImageLinks(root *os.Root, links []imageLink, create func(imageLink) error) error {
	// On Windows, Symlink checks the target to choose between a file link
	// and a directory link. Install links to existing targets first, including
	// links that become resolvable after installing another link.
	for runtime.GOOS == "windows" && len(links) > 0 {
		pending := links[:0]
		for _, link := range links {
			// Keep parent steps so Stat resolves them after following links.
			target := filepath.Dir(link.name) + string(filepath.Separator) + link.target
			if _, err := root.Stat(target); err != nil {
				pending = append(pending, link)
				continue
			}
			if err := create(link); err != nil {
				return err
			}
		}
		if len(pending) == len(links) {
			break
		}
		links = pending
	}
	for _, link := range links {
		if err := create(link); err != nil {
			return err
		}
	}
	return nil
}
