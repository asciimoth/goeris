package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/internal/batch"
	"github.com/asciimoth/zpgo"
	"golang.org/x/term"
)

// parseBlockCommand accepts storage flags before and after the subcommand.
// Operation flags follow the subcommand and precede the block hashes.
func parseBlockCommand(flags *flag.FlagSet, args []string) (commandOptions, error) {
	command := commandOptions{name: "block"}
	if err := flags.Parse(args); err != nil {
		return command, fmt.Errorf("block: %w", err)
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		return command, flag.ErrHelp
	}
	switch args[0] {
	case "get":
		flags.IntVar(&command.batchSize, "batch-size", 0, "maximum blocks per request")
	case "put", "prefetch", "update":
		flags.BoolVar(&command.blockOptions.HighPriority, "high-priority", false, "set the high-priority flag")
		flags.BoolVar(&command.blockOptions.Root, "root", false, "set the root flag")
	case "check":
	default:
		return command, fmt.Errorf("block: unknown subcommand %q", args[0])
	}
	command.name += " " + args[0]
	if err := flags.Parse(args[1:]); err != nil {
		return command, fmt.Errorf("%s: %w", command.name, err)
	}
	switch command.name {
	case "block put":
		if flags.NArg() > 0 {
			return command, fmt.Errorf("block put: unexpected arguments: %q", flags.Args())
		}
	case "block get":
		if command.batchSize < 0 {
			return command, errors.New("block get: --batch-size must be at least 0")
		}
	default:
		if flags.NArg() == 0 {
			return command, fmt.Errorf("%s: expected at least one block hash", command.name)
		}
	}
	command.hashes = flags.Args()
	return command, nil
}

// runBlock executes a parsed block operation on the configured store.
func (command commandOptions) runBlock(progress *zpgo.Progress, store eris.BlockStore, input io.Reader) error {
	switch command.name {
	case "block put":
		return blockPut(progress, store, input, command.blockOptions)
	case "block get":
		return blockGet(progress, store, command.hashes, command.batchSize)
	case "block prefetch":
		return blockPrefetch(store, command.hashes, command.blockOptions)
	case "block check":
		return blockCheck(progress, store, command.hashes)
	case "block update":
		return blockUpdate(progress, store, command.hashes, command.blockOptions)
	default:
		return fmt.Errorf("unknown block operation %q", command.name)
	}
}

// blockProgress reserves stdout for block data. For multiple reads,
// report to the inherited progress pipe or display progress on a stderr terminal.
func blockProgress(total int) (*zpgo.Progress, error) {
	if total < 2 {
		return &zpgo.Progress{}, nil
	}
	opts := zpgo.Options{RootName: "Blocks written", EstimatedTotalItems: uint64(total)}
	if descriptor, inherited := os.LookupEnv("ZIG_PROGRESS"); inherited {
		// The binary progress protocol needs a pipe separate from the standard streams.
		switch strings.TrimLeft(descriptor, "0") {
		case "", "1", "2":
			return &zpgo.Progress{}, nil
		}
	} else if os.Getenv("ZPG_NO_PROGRESS") != "1" && term.IsTerminal(int(os.Stderr.Fd())) {
		opts.Renderer = &zpgo.RendererOptions{Writer: os.Stderr}
	}
	return zpgo.New(opts)
}

// blockPut reads at most one byte beyond the largest block to detect excess
// input. It stores the bytes unchanged and writes the reference on its own line.
func blockPut(progress *zpgo.Progress, sink eris.BlockSink, input io.Reader, options eris.BlockOptions) error {
	const maxSize = int(eris.BlockSize32KiB)
	data, err := io.ReadAll(io.LimitReader(input, int64(maxSize)+1))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	if len(data) > maxSize {
		return fmt.Errorf("%w: stdin exceeds %d bytes", eris.ErrInvalidBlockSize, maxSize)
	}
	if !eris.ValidBlockLength(len(data)) {
		return fmt.Errorf("%w: stdin has %d bytes, want %d or %d", eris.ErrInvalidBlockSize, len(data), eris.BlockSize1KiB, maxSize)
	}
	ref, err := eris.PutBlock(context.Background(), sink, data, eris.NoTTL, options)
	if err != nil {
		return fmt.Errorf("store block: %w", err)
	}
	if _, err := progress.PrintfStdout("%s\n", ref); err != nil {
		return fmt.Errorf("write hash to stdout: %w", err)
	}
	return nil
}

// blockGet fetches and writes one group at a time, within the storage limit.
func blockGet(progress *zpgo.Progress, source eris.BlockSource, hashes []string, batchSize int) error {
	limit := source.BatchSize()
	switch {
	case batchSize == 0 || limit < 0:
		batchSize = batch.Limit(limit)
	case limit > 0:
		batchSize = min(batchSize, limit)
	}
	ctx := context.Background()
	return forEachBlockBatch(hashes, batchSize, eris.BlockOptions{}, func(requests []eris.BlockRequest) error {
		return writeBlockBatch(ctx, progress, source, requests)
	})
}

// blockPrefetch sends best-effort hints within the storage batch limit. The
// source can discard hints and returns without waiting for any block reads.
func blockPrefetch(source eris.BlockSource, hashes []string, options eris.BlockOptions) error {
	ctx := context.Background()
	return forEachBlockBatch(hashes, batch.Limit(source.BatchSize()), options, func(requests []eris.BlockRequest) error {
		source.PrefetchBlock(ctx, requests)
		return nil
	})
}

// forEachBlockBatch parses one group at a time. If a hash is invalid, it
// submits the valid prefix of the group before returning the parse error.
func forEachBlockBatch(hashes []string, batchSize int, options eris.BlockOptions, submit func([]eris.BlockRequest) error) error {
	requests := make([]eris.BlockRequest, 0, min(batchSize, len(hashes)))
	for group := range slices.Chunk(hashes, batchSize) {
		requests = requests[:0]
		var parseErr error
		for _, hash := range group {
			ref, err := parseBlockHash(hash)
			if err != nil {
				parseErr = err
				break
			}
			requests = append(requests, eris.BlockRequest{Reference: ref, Options: options})
		}
		if len(requests) > 0 {
			if err := submit(requests); err != nil {
				return err
			}
		}
		if parseErr != nil {
			return parseErr
		}
	}
	return nil
}

// blockCheck reads each block through the storage chain and prints its hash
// and stored flags in input order.
func blockCheck(progress *zpgo.Progress, source eris.BlockSource, hashes []string) error {
	ctx := context.Background()
	for _, hash := range hashes {
		ref, err := parseBlockHash(hash)
		if err != nil {
			return err
		}
		_, err, options := eris.GetBlock(ctx, source, ref, eris.NoTTL, eris.BlockOptions{})
		if err != nil {
			return fmt.Errorf("fetch block %s: %w", ref, err)
		}
		if _, err := progress.PrintfStdout("%s high-priority=%t root=%t\n", ref, options.HighPriority, options.Root); err != nil {
			return fmt.Errorf("write options for block %s to stdout: %w", ref, err)
		}
	}
	return nil
}

// blockUpdate validates all hashes before replacing any flags. It then
// updates the blocks in input order and reports each result.
func blockUpdate(progress *zpgo.Progress, sink eris.BlockSink, hashes []string, options eris.BlockOptions) error {
	refs := make([]eris.Reference, len(hashes))
	for i, hash := range hashes {
		ref, err := parseBlockHash(hash)
		if err != nil {
			return err
		}
		refs[i] = ref
	}
	ctx := context.Background()
	for _, ref := range refs {
		changed, err := eris.UpdateBlockOptions(ctx, sink, ref, eris.NoTTL, options)
		if err != nil {
			return fmt.Errorf("update options for block %s: %w", ref, err)
		}
		if _, err := progress.PrintfStdout("%s changed=%t\n", ref, changed); err != nil {
			return fmt.Errorf("write update result for block %s to stdout: %w", ref, err)
		}
	}
	return nil
}

// parseBlockHash adds the original argument to a reference parse error.
func parseBlockHash(hash string) (eris.Reference, error) {
	ref, err := eris.ParseReference(hash)
	if err != nil {
		return ref, fmt.Errorf("invalid block hash %q: %w", hash, err)
	}
	return ref, nil
}

// writeBlockBatch fetches a nonempty batch. A batch can contain results after
// a missing block. Write only the blocks before the first failed request.
func writeBlockBatch(ctx context.Context, progress *zpgo.Progress, source eris.BlockSource, requests []eris.BlockRequest) error {
	blocks, err := source.GetBlock(ctx, requests)
	if len(blocks) > len(requests) || err == nil && len(blocks) != len(requests) {
		return fmt.Errorf("fetch batch starting at block %s: %w", requests[0].Reference, eris.ErrInvalidBatchResult)
	}
	for i, block := range blocks {
		ref := requests[i].Reference
		if block.Data == nil && errors.Is(err, eris.ErrMissingBlock) {
			return fmt.Errorf("fetch block %s: %w", ref, err)
		}
		if !eris.ValidBlockLength(len(block.Data)) {
			return fmt.Errorf("fetch block %s: %w", ref, eris.ErrInvalidBlockSize)
		}
		if _, err := progress.WriteStdout(block.Data); err != nil {
			return fmt.Errorf("write block %s to stdout: %w", ref, err)
		}
		progress.Root().CompleteOne()
	}
	if err != nil {
		if len(blocks) < len(requests) {
			return fmt.Errorf("fetch block %s: %w", requests[len(blocks)].Reference, err)
		}
		return fmt.Errorf("fetch batch starting at block %s: %w", requests[0].Reference, err)
	}
	return nil
}
