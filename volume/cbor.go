package volume

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/cbor"
)

func (v *Volume) readCBOR(ctx context.Context, ref eris.Reference) ([]byte, error) {
	info, err := v.archive.Stat()
	if err != nil {
		return nil, err
	}
	// Each call has its own offset. An append cannot extend the read forever.
	reader := io.NewSectionReader(v.archive, 0, info.Size())
	block, err := findCBOR(ctx, reader, ref)
	if err == nil || errors.Is(err, eris.ErrMissingBlock) {
		// A concurrent truncation must not turn a valid sequence prefix into
		// a successful lookup. The file must remain unchanged during reads.
		position, _ := reader.Seek(0, io.SeekCurrent)
		if position != info.Size() {
			return nil, errors.Join(cbor.ErrInvalidEncoding, io.ErrUnexpectedEOF)
		}
	}
	return block, err
}

// findCBOR keeps at most two blocks and the references in the current map.
// It validates the complete container before it returns a matching block.
func findCBOR(ctx context.Context, reader io.Reader, ref eris.Reference) ([]byte, error) {
	d := cborReader{ctx: ctx, reader: bufio.NewReader(contextReader{ctx, reader}), ref: ref}
	if err := d.container(); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			err = cbor.ErrInvalidEncoding
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.block == nil {
		return nil, errors.Join(eris.ErrMissingBlock, fs.ErrNotExist)
	}
	return d.block, nil
}

// Check each underlying read, including retries after short reads in a CBOR
// header or byte string. Parser checks alone do not cover these retries.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type cborReader struct {
	ctx    context.Context
	reader *bufio.Reader
	ref    eris.Reference
	block  []byte
}

func (d *cborReader) peek() (byte, error) {
	if err := d.ctx.Err(); err != nil {
		return 0, err
	}
	b, err := d.reader.Peek(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// head does not allocate from an untrusted CBOR length.
func (d *cborReader) head() (major byte, value uint64, indefinite bool, err error) {
	b, err := d.peek()
	if err != nil {
		return 0, 0, false, err
	}
	_, _ = d.reader.ReadByte()
	major = b >> 5
	additional := b & 31
	switch {
	case additional < 24:
		return major, uint64(additional), false, nil
	case additional <= 27:
		for range 1 << (additional - 24) {
			b, err = d.reader.ReadByte()
			if err != nil {
				return 0, 0, false, err
			}
			value = value<<8 | uint64(b)
		}
		return major, value, false, nil
	case additional == 31:
		return major, 0, true, nil
	default:
		return 0, 0, false, cbor.ErrInvalidEncoding
	}
}

func (d *cborReader) container() error {
	b, err := d.peek()
	if err == io.EOF { // Empty untagged sequence.
		return nil
	}
	if err != nil {
		return err
	}
	tagged := false
	selfDescribed := false
	if b>>5 == 6 {
		_, tag, indefinite, err := d.head()
		if err != nil {
			return err
		}
		if indefinite {
			return cbor.ErrInvalidEncoding
		}
		if tag == cbor.SelfDescribedTag {
			selfDescribed = true
			major, next, indef, err := d.head()
			if err != nil {
				return err
			}
			if major != 6 || indef || next != cbor.Tag {
				return cbor.ErrInvalidEncoding
			}
			tag = next
		}
		switch tag {
		case cbor.Tag:
			tagged = true
		case eris.CapabilityCBORTag:
			// An untagged sequence can start with a capability.
			if err := d.capability(); err != nil {
				return err
			}
			return d.sequence()
		default:
			return cbor.ErrInvalidEncoding
		}
		b, err = d.peek()
		if err != nil {
			return err
		}
	}
	if b>>5 == 4 {
		_, count, indefinite, err := d.head()
		if err != nil {
			return err
		}
		for ; indefinite || count > 0; count-- {
			if indefinite {
				b, err := d.peek()
				if err != nil {
					return err
				}
				if b == 0xff {
					_, _ = d.reader.ReadByte()
					break
				}
			}
			if err := d.element(); err != nil {
				return err
			}
		}
		_, err = d.peek()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		return cbor.ErrInvalidEncoding // Trailing data after an array.
	}
	if selfDescribed {
		return cbor.ErrInvalidEncoding
	}
	if tagged {
		if b != 0xf6 {
			return cbor.ErrInvalidEncoding
		}
		_, _ = d.reader.ReadByte()
	}
	return d.sequence()
}

func (d *cborReader) sequence() error {
	for {
		if _, err := d.peek(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := d.element(); err != nil {
			return err
		}
	}
}

func (d *cborReader) element() error {
	b, err := d.peek()
	if err != nil {
		return err
	}
	switch b >> 5 {
	case 2:
		block, err := d.byteString(int(eris.BlockSize32KiB))
		if err != nil {
			return err
		}
		if len(block) == len(eris.Reference{}) {
			return nil
		}
		ref, err := eris.ReferenceOf(block)
		if err != nil {
			return cbor.ErrInvalidEncoding
		}
		d.match(ref, block)
		return nil
	case 5:
		return d.blockMap()
	case 6:
		_, tag, indefinite, err := d.head()
		if err != nil {
			return err
		}
		if indefinite || tag != eris.CapabilityCBORTag {
			return cbor.ErrInvalidEncoding
		}
		return d.capability()
	default:
		return cbor.ErrInvalidEncoding
	}
}

func (d *cborReader) capability() error {
	data, err := d.byteString(eris.CapabilitySize)
	if err != nil {
		return err
	}
	if _, err := eris.ParseCapability(data); err != nil {
		return cbor.ErrInvalidEncoding
	}
	return nil
}

func (d *cborReader) blockMap() error {
	_, count, indefinite, err := d.head()
	if err != nil {
		return err
	}
	seen := make(map[eris.Reference]struct{})
	for ; indefinite || count > 0; count-- {
		if indefinite {
			b, err := d.peek()
			if err != nil {
				return err
			}
			if b == 0xff {
				_, _ = d.reader.ReadByte()
				return nil
			}
		}
		key, err := d.byteString(len(eris.Reference{}))
		if err != nil {
			return err
		}
		if len(key) != len(eris.Reference{}) {
			return cbor.ErrInvalidEncoding
		}
		ref := eris.Reference(key)
		if _, duplicate := seen[ref]; duplicate {
			return cbor.ErrInvalidEncoding
		}
		block, err := d.byteString(int(eris.BlockSize32KiB))
		if err != nil {
			return err
		}
		if err := eris.VerifyBlock(block, ref); err != nil {
			return cbor.ErrInvalidEncoding
		}
		seen[ref] = struct{}{}
		d.match(ref, block)
	}
	return nil
}

func (d *cborReader) match(ref eris.Reference, block []byte) {
	if d.block == nil && ref == d.ref {
		d.block = block
	}
}

func (d *cborReader) byteString(limit int) ([]byte, error) {
	major, length, indefinite, err := d.head()
	if err != nil {
		return nil, err
	}
	if major != 2 {
		return nil, cbor.ErrInvalidEncoding
	}
	var data []byte
	for {
		if indefinite {
			b, err := d.peek()
			if err != nil {
				return nil, err
			}
			if b == 0xff {
				_, _ = d.reader.ReadByte()
				return data, nil
			}
			var chunkIndefinite bool
			major, length, chunkIndefinite, err = d.head()
			if err != nil {
				return nil, err
			}
			if major != 2 || chunkIndefinite {
				return nil, cbor.ErrInvalidEncoding
			}
		}
		if length > uint64(limit-len(data)) {
			return nil, cbor.ErrInvalidEncoding
		}
		start := len(data)
		data = append(data, make([]byte, int(length))...)
		if _, err := io.ReadFull(d.reader, data[start:]); err != nil {
			return nil, err
		}
		if !indefinite {
			return data, nil
		}
	}
}
