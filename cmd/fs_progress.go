package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/zpgo"
)

// Give short image operations time to finish before drawing a progress frame.
const imageProgressInterval = time.Second

// imageMeter counts successful block reads and issued hints, including those
// used to load indexes. Store calls only update counters. The display samples
// them once per second and reports deltas for the current command.
type imageMeter struct {
	read, hints atomic.Uint64
}

type meteredImageBlocks struct {
	eris.BlockStore
	meter *imageMeter
}

func (m *imageMeter) wrap(s eris.BlockStore) eris.BlockStore {
	return meteredImageBlocks{s, m}
}

func (s meteredImageBlocks) GetBlock(ctx context.Context, requests []eris.BlockRequest) ([]eris.Block, error) {
	blocks, err := s.BlockStore.GetBlock(ctx, requests)
	var read uint64
	for _, b := range blocks {
		if b.Data != nil {
			read++
		}
	}
	s.meter.read.Add(read)
	return blocks, err
}

func (s meteredImageBlocks) PrefetchBlock(ctx context.Context, requests []eris.BlockRequest) {
	s.BlockStore.PrefetchBlock(ctx, requests)
	s.meter.hints.Add(uint64(len(requests)))
}

// replActivity delays the first display until the command takes one second.
// Keep the inherited Zig pipe for the session. Each terminal display belongs
// to one command so Close can erase it before the next prompt.
func (m *imageMeter) replActivity(idle *zpgo.Progress, name string) (*zpgo.Progress, func(), error) {
	if !idle.Enabled() {
		return idle, func() {}, nil
	}
	active := idle
	_, inherited := os.LookupEnv("ZIG_PROGRESS")
	if !inherited {
		var err error
		active, err = commandProgress(zpgo.Options{InitialDelay: imageProgressInterval})
		if err != nil {
			return nil, nil, err
		}
	}
	active.Root().SetCompletedItems(0)
	stop := m.activity(active, name, inherited)
	return active, func() {
		stop()
		if active != idle {
			_ = active.Close()
		}
	}, nil
}

// activity shows elapsed time while a backend is busy in an opaque
// operation, such as opening an index or requesting a subtree prefetch.
// File transfers also report measured progress on a separate child node.
func (m *imageMeter) activity(p *zpgo.Progress, name string, manualPipe bool) func() {
	if m == nil {
		return func() {}
	}
	start := time.Now()
	node := p.Root()
	initialRead, initialHints := m.read.Load(), m.hints.Load()
	node.SetName("fs " + name)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(imageProgressInterval)
		defer tick.Stop()
		reported := false
		for {
			select {
			case <-done:
				// Retire the last active subtree in the parent display.
				if reported {
					node.SetName("fs " + name + " finished")
					_ = p.Flush()
				}
				return
			case now := <-tick.C:
				label := fmt.Sprintf("fs %s (%s)", name, now.Sub(start).Round(time.Second))
				if read, hints := m.read.Load()-initialRead, m.hints.Load()-initialHints; read+hints > 0 {
					label += fmt.Sprintf(" %d blocks read, %d hints", read, hints)
				}
				node.SetName(label)
				if manualPipe {
					reported = true
					_ = p.Flush()
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// Count KiB to cover multi-gigabyte files within Zig's 32-bit item counts.
type imageReader struct {
	io.Reader
	node zpgo.Node
	size int64
}

func (r *imageReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.size += int64(n)
	r.node.SetCompletedItems(kib(r.size))
	return n, err
}

func kib(n int64) uint64 {
	if n <= 0 {
		return 0
	}
	return 1 + uint64(n-1)/1024
}
