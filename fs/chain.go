package fs

import (
	"context"
	"errors"
	stdfs "io/fs"
	"path"
	"reflect"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
)

// ErrNoBackend means a chain has no members.
var ErrNoBackend = errors.New("eris-fs: no backend")

// CombinedStore tries each image store in order until one succeeds. If all
// stores fail, it returns the last error. Cancellation stops fallback.
// NewBuilder returns the selected builder. OpenFS retains the first image
// that opens. Its view opens other backends on demand for fallback and
// Prefetch. Each operation uses the context supplied to that call.
type CombinedStore struct {
	stores      []ImageStore
	decodedOnly bool
}

// NewCombinedStore copies the store list and omits nil members.
func NewCombinedStore(stores ...ImageStore) *CombinedStore {
	decodedOnly := true
	var members []ImageStore
	for _, s := range stores {
		if s != nil {
			switch store := s.(type) {
			case *Store:
			case *CombinedStore:
				decodedOnly = decodedOnly && store != nil && store.decodedOnly
			default:
				decodedOnly = false
			}
			members = append(members, s)
		}
	}
	return &CombinedStore{stores: members, decodedOnly: decodedOnly}
}

// CombinedFileSystem tries each complete file operation in order. It returns
// the first success or the last error. Cancellation stops fallback. Open
// files and Sub views belong to the selected backend.
type CombinedFileSystem struct {
	images []imageOpener
	// All openers created by openImages select the same capability.
	sameCapability bool
	// location identifies the directory shared by all decoded members.
	location decodedLocation
}

// The zero location denotes a view whose index or directory is unknown.
type decodedLocation struct {
	index eris.Capability
	dir   string // Canonical path relative to this index.
}

func locationOf(view FileSystem) decodedLocation {
	switch image := view.(type) {
	case *FS:
		if image == nil || image.root == nil || image.root.capability == (eris.Capability{}) {
			return decodedLocation{}
		}
		dir := image.root
		location := decodedLocation{dir.capability, image.prefix}
		// Sub has already resolved each component to an immutable, loaded
		// directory. Use the innermost index, which can also be opened
		// directly or included at another path.
		for rest := image.prefix; rest != "." && rest != ""; {
			name, tail, _ := strings.Cut(rest, "/")
			dir = dir.children[name]
			if dir.kind == Subdirectory {
				location = decodedLocation{dir.capability, tail}
				if tail == "" {
					location.dir = "."
				}
			}
			rest = tail
		}
		return location
	case *CombinedFileSystem:
		if image != nil {
			return image.location
		}
	}
	return decodedLocation{}
}

type imageOpener func(context.Context) (FileSystem, error)

func fixedImage(image FileSystem) imageOpener {
	return func(context.Context) (FileSystem, error) { return image, nil }
}

// NewCombinedFileSystem copies the view list and omits nil members. When the
// views decode the same image at the same directory, ReadLink can use the
// selected decoder. Other multi-image views require Sub to export links.
func NewCombinedFileSystem(images ...FileSystem) *CombinedFileSystem {
	var members []imageOpener
	var location decodedLocation
	for _, image := range images {
		if image == nil {
			continue
		}
		if len(members) == 0 {
			location = locationOf(image)
		} else if location != locationOf(image) {
			location = decodedLocation{}
		}
		members = append(members, fixedImage(image))
	}
	return &CombinedFileSystem{images: members, location: location}
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return stdfs.ErrInvalid
	}
	return ctx.Err()
}

func firstResult[B, T any](ctx context.Context, backends []B, call func(B) (T, error)) (T, error) {
	var zero T
	if err := checkContext(ctx); err != nil {
		return zero, err
	}
	last := ErrNoBackend
	for _, b := range backends {
		v, err := call(b)
		if err == nil {
			return v, nil
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		last = err
	}
	return zero, last
}

// EncodeIndex stops if a failed backend changed its input. Give it private
// copies of both slice levels so it cannot also overwrite the caller's index.
func (s *CombinedStore) EncodeIndex(ctx context.Context, index Index, secret eris.Secret) (eris.Capability, error) {
	if err := checkContext(ctx); err != nil {
		return eris.Capability{}, err
	}
	last := ErrNoBackend
	for i, store := range s.stores {
		entries := slices.Clone(index.Entries)
		for i := range entries {
			entries[i].Path = slices.Clone(entries[i].Path)
		}
		attempt := Index{Entries: entries}
		cap, err := store.EncodeIndex(ctx, attempt, secret)
		if err == nil {
			return cap, nil
		}
		if err := ctx.Err(); err != nil {
			return eris.Capability{}, err
		}
		if i < len(s.stores)-1 && !reflect.DeepEqual(attempt, index) {
			return eris.Capability{}, err
		}
		last = err
	}
	return eris.Capability{}, last
}

// Prefetch calls every backend in order. One successful hint is sufficient.
// If all hints fail it returns the last error.
func (s *CombinedFileSystem) Prefetch(ctx context.Context, name string) error {
	if ctx == nil {
		return stdfs.ErrInvalid
	}
	if len(s.images) == 0 && ctx.Err() != nil {
		return ctx.Err()
	}
	last := ErrNoBackend
	success := false
	for _, open := range s.images {
		var image FileSystem
		image, last = open(ctx)
		if last == nil {
			last = image.Prefetch(ctx, name)
		}
		if last == nil {
			success = true
		}
	}
	if success {
		return nil
	}
	return last
}

func (s *CombinedStore) NewBuilder(secret eris.Secret) (ImageBuilder, error) {
	return firstResult(context.Background(), s.stores, func(b ImageStore) (ImageBuilder, error) { return b.NewBuilder(secret) })
}

// openImages retains the first successful lookup. Lazy openers use the same
// capability and limit even when the initial context has expired. Set decodedOnly
// when every opener decodes the requested root index with Store.
func openImages[B any](ctx context.Context, backends []B, decodedOnly bool, open func(context.Context, B) (FileSystem, error)) (FileSystem, error) {
	selected := -1
	image, err := firstResult(ctx, backends, func(b B) (FileSystem, error) {
		selected++
		return open(ctx, b)
	})
	if err != nil || len(backends) == 1 {
		return image, err
	}
	views := make([]imageOpener, len(backends))
	for i, store := range backends {
		if i == selected {
			views[i] = fixedImage(image)
		} else {
			views[i] = func(ctx context.Context) (FileSystem, error) { return open(ctx, store) }
		}
	}
	combined := &CombinedFileSystem{images: views, sameCapability: true}
	if decodedOnly {
		combined.location = locationOf(image)
	}
	return combined, nil
}

func (s *CombinedStore) OpenFS(ctx context.Context, cap eris.Capability) (FileSystem, error) {
	return openImages(ctx, s.stores, s.decodedOnly, func(ctx context.Context, store ImageStore) (FileSystem, error) {
		return store.OpenFS(ctx, cap)
	})
}

func (s *CombinedStore) OpenFSWithLimit(ctx context.Context, cap eris.Capability, limit int64) (FileSystem, error) {
	return openImages(ctx, s.stores, s.decodedOnly, func(ctx context.Context, store ImageStore) (FileSystem, error) {
		return store.OpenFSWithLimit(ctx, cap, limit)
	})
}

func firstImageResult[T any](ctx context.Context, s *CombinedFileSystem, call func(FileSystem) (T, error)) (T, error) {
	return firstResult(ctx, s.images, func(open imageOpener) (T, error) {
		image, err := open(ctx)
		if err != nil {
			var zero T
			return zero, err
		}
		return call(image)
	})
}

func (s *CombinedStore) DecodeIndex(ctx context.Context, cap eris.Capability) (Index, error) {
	return firstResult(ctx, s.stores, func(b ImageStore) (Index, error) { return b.DecodeIndex(ctx, cap) })
}

func (s *CombinedStore) DecodeIndexWithLimit(ctx context.Context, cap eris.Capability, limit int64) (Index, error) {
	return firstResult(ctx, s.stores, func(b ImageStore) (Index, error) { return b.DecodeIndexWithLimit(ctx, cap, limit) })
}

func (s *CombinedFileSystem) Open(ctx context.Context, name string) (stdfs.File, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (stdfs.File, error) { return b.Open(ctx, name) })
}

func (s *CombinedFileSystem) OpenFile(ctx context.Context, name string) (ReadSeekCloser, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (ReadSeekCloser, error) { return b.OpenFile(ctx, name) })
}

func (s *CombinedFileSystem) ReadFile(ctx context.Context, name string) ([]byte, error) {
	return firstImageResult(ctx, s, func(b FileSystem) ([]byte, error) { return b.ReadFile(ctx, name) })
}

func (s *CombinedFileSystem) ReadDir(ctx context.Context, name string) ([]stdfs.DirEntry, error) {
	return firstImageResult(ctx, s, func(b FileSystem) ([]stdfs.DirEntry, error) { return b.ReadDir(ctx, name) })
}

func (s *CombinedFileSystem) Stat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (stdfs.FileInfo, error) { return b.Stat(ctx, name) })
}

func (s *CombinedFileSystem) Lstat(ctx context.Context, name string) (stdfs.FileInfo, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (stdfs.FileInfo, error) { return b.Lstat(ctx, name) })
}

func (s *CombinedFileSystem) Sub(ctx context.Context, name string) (FileSystem, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (FileSystem, error) { return b.Sub(ctx, name) })
}

// ReadLink exports a target only if the view has one backend or all backends
// were opened for the same capability. A native client resolves the returned
// target in a separate lookup, which can select a different backend.
func (s *CombinedFileSystem) ReadLink(ctx context.Context, name string) (string, error) {
	target, err := firstImageResult(ctx, s, func(b FileSystem) (string, error) { return b.ReadLink(ctx, name) })
	if err != nil || len(s.images) == 1 || s.location.dir != "" {
		return target, err
	}
	if !s.sameCapability {
		return "", pathError("readlink", name, stdfs.ErrNotExist)
	}
	for part := range strings.SplitSeq(target, "/") {
		if part == ".." {
			if err := s.checkTargetParents(ctx, name, target); err != nil {
				return "", pathError("readlink", name, err)
			}
			break
		}
	}
	return target, nil
}

// A mount adapter can resolve each path component in a different backend.
// Check each backend once, including the members of nested chains.
func (s *CombinedFileSystem) checkTargetParents(ctx context.Context, name, target string) error {
	for _, open := range s.images {
		image, err := open(ctx)
		if err != nil {
			return err
		}
		if combined, ok := image.(*CombinedFileSystem); ok {
			err = combined.checkTargetParents(ctx, name, target)
		} else {
			err = checkTargetParents(ctx, image, name, target)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Check directories removed by ".." before a client can clean or follow the
// target across backends. A parent step can leave a directory entered by this
// target, including a nested image. Steps above the link's own directory
// also need proof that they stay in its index.
func checkTargetParents(ctx context.Context, image FileSystem, name, target string) error {
	parent := path.Dir(name)
	dir, shallowest := parent, parent
	run := target // Components since the last parent step.
	for rest := target; rest != ""; {
		part, tail, _ := strings.Cut(rest, "/")
		if part == ".." {
			descended := path.Join(dir, run[:len(run)-len(rest)])
			if descended != dir {
				if err := checkLinkDirectories(ctx, image, descended, descended); err != nil {
					return err
				}
			}
			if descended == "." {
				return stdfs.ErrNotExist
			}
			dir = path.Dir(descended)
			if descended == shallowest {
				shallowest = dir
			}
			run = tail
		}
		rest = tail
	}
	if shallowest != parent {
		return checkLinkDirectories(ctx, image, parent, shallowest)
	}
	return nil
}

// A client can get a directory alias from one backend and its child's link
// from another. The depth of the link in either backend alone is insufficient.
// Check each prefix from the root so Lstat never follows an unchecked alias.
// Each prefix below ancestor must also be an ordinary directory.
func checkLinkDirectories(ctx context.Context, image FileSystem, dir, ancestor string) error {
	for i := 0; i <= len(dir); i++ {
		if i < len(dir) && dir[i] != '/' {
			continue
		}
		prefix := dir[:i]
		info, err := image.Lstat(ctx, prefix)
		if errors.Is(err, stdfs.ErrNotExist) {
			// A missing index can also report ErrNotExist. Only skip
			// this backend if its parent directory confirms absence.
			entries, listErr := image.ReadDir(ctx, path.Dir(prefix))
			if listErr != nil {
				return listErr
			}
			base := path.Base(prefix)
			if !slices.ContainsFunc(entries, func(e stdfs.DirEntry) bool { return e.Name() == base }) {
				break
			}
		}
		if err != nil {
			return err
		}
		if info.Mode()&stdfs.ModeSymlink != 0 {
			return stdfs.ErrNotExist
		}
		if !info.IsDir() {
			break
		}
		if ancestor == "." || len(prefix) > len(ancestor) {
			// Only a decoded index identifies an ordinary directory.
			// A mounted tree can have an image root at any directory.
			node, ok := info.(*node)
			if !ok || node.kind != directoryType {
				return stdfs.ErrNotExist
			}
		}
	}
	return nil
}

func (s *CombinedFileSystem) ResolvePath(ctx context.Context, name string) (string, error) {
	return firstImageResult(ctx, s, func(b FileSystem) (string, error) { return b.ResolvePath(ctx, name) })
}
