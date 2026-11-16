// Package streamio provides shared stream I/O helpers.
package streamio

import "io"

// ReadFull fills a buffer or returns the source's terminal error.
// Unlike io.ReadFull, EOF and other errors are preserved even when they
// accompany a full buffer. Thus a source's ErrUnexpectedEOF cannot be mistaken
// for a valid file ending.
// Only io.EOF itself is a normal end; wrapped or joined errors must propagate.
// It returns io.ErrNoProgress after 100 consecutive empty reads.
func ReadFull(content io.Reader, buffer []byte) (int, error) {
	n, empty := 0, 0
	for n < len(buffer) {
		count, err := content.Read(buffer[n:])
		n += count
		if err != nil {
			return n, err
		}
		if count == 0 {
			empty++
			if empty == 100 {
				return n, io.ErrNoProgress
			}
		} else {
			empty = 0
		}
	}
	return n, nil
}
