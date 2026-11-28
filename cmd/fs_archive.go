package main

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/flate"
	"compress/gzip"
	"compress/lzw"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	erisfs "github.com/asciimoth/goeris/fs"
	"github.com/asciimoth/goeris/internal/streamio"
)

type imageArchive struct {
	format string
	stem   string // Name for --preserve-root, without the archive suffix.
}

// Short compression suffixes and their .tar.* forms select the same tar stream.
func parseImageArchive(name string) (imageArchive, error) {
	base := filepath.Base(name)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	format := strings.TrimPrefix(strings.ToLower(ext), ".")
	switch format {
	case "zip", "tar":
		return imageArchive{format, stem}, nil
	case "tgz":
		return imageArchive{"gz", stem}, nil
	case "tbz", "tbz2":
		return imageArchive{"bz2", stem}, nil
	case "gzip":
		format = "gz"
	case "bzip2":
		format = "bz2"
	case "zz":
		format = "zlib"
	case "gz", "bz2", "zlib", "deflate", "lzw":
	default:
		return imageArchive{}, fmt.Errorf("unsupported archive suffix in %q", name)
	}
	if strings.EqualFold(filepath.Ext(stem), ".tar") {
		stem = stem[:len(stem)-len(".tar")]
	}
	return imageArchive{format, stem}, nil
}

func tarInput(format string, r io.Reader) (io.ReadCloser, error) {
	switch format {
	case "tar":
		return io.NopCloser(r), nil
	case "gz":
		return gzip.NewReader(r)
	case "bz2":
		return io.NopCloser(bzip2.NewReader(r)), nil
	case "zlib":
		return zlib.NewReader(r)
	case "deflate":
		return flate.NewReader(r), nil
	case "lzw":
		return lzw.NewReader(r, lzw.LSB, 8), nil
	default:
		return nil, fmt.Errorf("unsupported tar compression %q", format)
	}
}

func tarOutput(format string, w io.Writer) (io.WriteCloser, error) {
	switch format {
	case "gz":
		return gzip.NewWriter(w), nil
	case "zlib":
		return zlib.NewWriter(w), nil
	case "deflate":
		return flate.NewWriter(w, flate.DefaultCompression)
	case "lzw":
		return lzw.NewWriter(w, lzw.LSB, 8), nil
	default:
		return nil, fmt.Errorf("cannot write tar compression %q", format)
	}
}

func (tree *imageBuild) readArchive(name string, info fs.FileInfo, format string) error {
	f, err := tree.host.OpenFile(name, streamio.ReadFileFlags, 0)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return fmt.Errorf("archive %q changed before it could be opened", name)
	}
	a := archiveBuild{tree: tree, dirs: make(map[string]bool)}
	if format == "zip" {
		reader, err := zip.NewReader(f, opened.Size())
		if err != nil {
			return err
		}
		for _, member := range reader.File {
			if err := a.readZipMember(member); err != nil {
				return fmt.Errorf("%q: %w", member.Name, err)
			}
		}
	} else {
		r, err := tarInput(format, f)
		if err != nil {
			return err
		}
		defer func() { _ = r.Close() }()
		if err := a.readTar(tar.NewReader(r)); err != nil {
			return err
		}
		// Read past tar padding so the decompressor verifies its trailer.
		if _, err := io.Copy(io.Discard, r); err != nil {
			return err
		}
	}
	for dir, hasChildren := range a.dirs {
		if !hasChildren {
			if err := tree.addEmptyDirectory(dir); err != nil {
				return fmt.Errorf("%q: %w", dir, err)
			}
		}
	}
	return nil
}

type archiveBuild struct {
	tree *imageBuild
	dirs map[string]bool // An explicit directory can appear after its children.
}

// Accept the ./ prefix used by tar tools. Reject path traversal and names
// that an extractor on another platform could interpret as absolute paths.
func archivePath(raw string, dir bool) (string, error) {
	name := raw
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	if dir {
		if name == "." || (name == "" && raw != "") {
			return ".", nil
		}
		name = strings.TrimSuffix(name, "/")
	}
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\\x00") ||
		(len(name) >= 2 && name[1] == ':' && ((name[0] >= 'a' && name[0] <= 'z') || (name[0] >= 'A' && name[0] <= 'Z'))) {
		return "", fmt.Errorf("invalid archive path %q", raw)
	}
	return name, nil
}

func (a *archiveBuild) memberPath(raw string, dir bool) (string, error) {
	name, err := archivePath(raw, dir)
	if err != nil || name == "." {
		return name, err
	}
	for parent := path.Dir(name); parent != "." && !a.dirs[parent]; parent = path.Dir(parent) {
		a.dirs[parent] = true
	}
	if dir {
		if _, exists := a.dirs[name]; !exists {
			a.dirs[name] = false
		}
	}
	return name, nil
}

func (a *archiveBuild) readTar(r *tar.Reader) error {
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		name, err := a.memberPath(h.Name, h.Typeflag == tar.TypeDir)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeGNUSparse:
			err = a.tree.addFile(name, h.FileInfo().Mode(), h.Size, r)
		case tar.TypeSymlink:
			err = a.tree.builder.AddSymlink(name, h.Linkname)
		default:
			return fmt.Errorf("%q: unsupported tar entry type %q", name, h.Typeflag)
		}
		if err != nil {
			return fmt.Errorf("%q: %w", name, err)
		}
	}
}

func (a *archiveBuild) readZipMember(member *zip.File) error {
	mode := member.Mode()
	name, err := a.memberPath(member.Name, mode.IsDir())
	if err != nil {
		return err
	}
	if mode.IsDir() {
		if member.UncompressedSize64 != 0 {
			return errors.New("ZIP directory has file data")
		}
		return nil
	}
	r, err := member.Open()
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	switch mode.Type() {
	case 0:
		return a.tree.addFile(name, mode, member.FileInfo().Size(), r)
	case fs.ModeSymlink:
		if member.UncompressedSize64 > uint64(erisfs.DefaultMaxIndexSize) {
			return errors.New("link target exceeds image index limit")
		}
		target, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		return a.tree.builder.AddSymlink(name, string(target))
	default:
		return fmt.Errorf("unsupported ZIP entry mode %s", mode)
	}
}

func (b imageBrowser) pack(dest, format string) error {
	parent, local, err := openImageDestination(dest, "archive")
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	f, err := parent.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	created, err := f.Stat()
	if err != nil {
		return errors.Join(err, f.Close())
	}
	err = errors.Join(b.writeArchive(format, f, created), f.Close())
	if err == nil {
		err = verifyImageOutput(dest, created)
	}
	if err != nil {
		return errors.Join(err, removeImageOutput(parent, local, created))
	}
	return nil
}

type writeArchiveMember func(name string, mode fs.FileMode, size int64, target string, data io.Reader) error

func (b imageBrowser) writeArchive(format string, w io.Writer, output fs.FileInfo) (err error) {
	var member writeArchiveMember
	if format == "zip" {
		z := zip.NewWriter(w)
		defer func() { err = errors.Join(err, z.Close()) }()
		member = zipArchiveWriter{z}.writeMember
	} else {
		if format != "tar" {
			compressed, openErr := tarOutput(format, w)
			if openErr != nil {
				return openErr
			}
			defer func() { err = errors.Join(err, compressed.Close()) }()
			w = compressed
		}
		t := tar.NewWriter(w)
		defer func() { err = errors.Join(err, t.Close()) }()
		member = tarArchiveWriter{t}.writeMember
	}
	return fs.WalkDir(erisfs.NewIOFS(b.ctx, b.view), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == "." {
			return err
		}
		if _, err := archivePath(name, entry.IsDir()); err != nil {
			return err
		}
		switch entry.Type() {
		case fs.ModeDir:
			return member(name, fs.ModeDir|0755, 0, "", nil)
		case fs.ModeSymlink:
			target, err := b.view.ReadLink(b.ctx, name)
			if err != nil {
				return err
			}
			return member(name, fs.ModeSymlink|0777, 0, target, nil)
		case 0:
			return b.transferFile(name, func(info fs.FileInfo, r io.Reader) error {
				if os.SameFile(info, output) {
					return fmt.Errorf("archive destination is inside the image at %q", name)
				}
				// Images store only the executable flag. A mounted view
				// can also expose permissions from the host file.
				mode := fs.FileMode(0644)
				if info.Mode().Perm()&0111 != 0 {
					mode = 0755
				}
				return member(name, mode, info.Size(), "", r)
			})
		default:
			return fmt.Errorf("%q: unsupported file mode %s", name, entry.Type())
		}
	})
}

type zipArchiveWriter struct{ *zip.Writer }

func (z zipArchiveWriter) writeMember(name string, mode fs.FileMode, size int64, target string, data io.Reader) error {
	// Images have no timestamps. Use the earliest date ZIP can represent.
	h := &zip.FileHeader{Name: name, Modified: time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)}
	h.SetMode(mode)
	switch mode.Type() {
	case fs.ModeDir:
		h.Name += "/"
	case fs.ModeSymlink:
		data = strings.NewReader(target)
		size = int64(len(target))
	default:
		h.Method = zip.Deflate
	}
	out, err := z.CreateHeader(h)
	if err != nil || data == nil {
		return err
	}
	n, err := io.Copy(out, data)
	if err == nil && n != size {
		return fmt.Errorf("expected %d bytes, copied %d", size, n)
	}
	return err
}

type tarArchiveWriter struct{ *tar.Writer }

func (t tarArchiveWriter) writeMember(name string, mode fs.FileMode, size int64, target string, data io.Reader) error {
	h := &tar.Header{Name: name, Mode: int64(mode.Perm()), Size: size, Typeflag: tar.TypeReg}
	switch mode.Type() {
	case fs.ModeDir:
		h.Typeflag = tar.TypeDir
	case fs.ModeSymlink:
		h.Typeflag, h.Linkname = tar.TypeSymlink, target
	}
	if err := t.WriteHeader(h); err != nil {
		return err
	}
	if data != nil {
		_, err := io.Copy(t, data)
		return err
	}
	return nil
}
