package fetch

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"github.com/asciimoth/goeris/internal/streamio"
)

// TempStorage creates empty, independent temporary buffers. Create can be
// called concurrently by different fetch requests. On error it releases any
// resources it acquired.
type TempStorage interface {
	Create(context.Context) (TempData, error)
}

// TempData holds one candidate until it is ready for encoding. Write appends
// data. After the last write, each Reader starts at the beginning. Use the
// readers in sequence before calling Close to release the storage.
type TempData interface {
	io.WriteCloser
	Reader() (io.Reader, error)
}

// NewTempData creates an empty buffer. A nil storage uses memory. The caller
// must close the buffer after use.
func NewTempData(ctx context.Context, storage TempStorage) (TempData, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if storage == nil {
		return &memoryTempData{}, nil
	}
	return storage.Create(ctx)
}

type memoryTempData struct{ bytes.Buffer }

func (data *memoryTempData) Reader() (io.Reader, error) {
	return bytes.NewReader(data.Bytes()), nil
}

func (data *memoryTempData) Close() error {
	data.Buffer = bytes.Buffer{}
	return nil
}

// TempDir stores each buffer in a separate file. The directory must exist.
// An empty TempDir uses the system temporary directory. Closing a buffer
// removes its file.
type TempDir string

func (dir TempDir) Create(ctx context.Context) (TempData, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := streamio.CreateTemp(string(dir), "eris-fetch-*")
	if err != nil {
		return nil, err
	}
	return &fileTempData{file}, nil
}

type fileTempData struct{ file *os.File }

func (data *fileTempData) Write(p []byte) (int, error) { return data.file.Write(p) }

func (data *fileTempData) Reader() (io.Reader, error) {
	info, err := data.file.Stat()
	if err != nil {
		return nil, err
	}
	return io.NewSectionReader(data.file, 0, info.Size()), nil
}

func (data *fileTempData) Close() error {
	return errors.Join(data.file.Close(), os.Remove(data.file.Name()))
}
