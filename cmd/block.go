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

// parseBlockCommand accepts storage flags around the subcommand name and
// registers the batch size flag for get. Flags precede any block hashes.
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
	case "put":
		command.name = "block put"
	case "get":
		command.name = "block get"
		flags.IntVar(&command.batchSize, "batch-size", 0, "maximum blocks per request")
	default:
		return command, fmt.Errorf("block: unknown subcommand %q", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return command, fmt.Errorf("%s: %w", command.name, err)
	}
	if command.name == "block put" && flags.NArg() > 0 {
		return command, fmt.Errorf("block put: unexpected arguments: %q", flags.Args())
	}
	if command.batchSize < 0 {
		return command, errors.New("block get: --batch-size must be at least 0")
	}
	command.hashes = flags.Args()
	return command, nil
}

// blockProgress reserves stdout for block data or hashes. For multiple reads,
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
func blockPut(progress *zpgo.Progress, sink eris.BlockSink, input io.Reader) error {
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
	ref, err := eris.PutBlock(context.Background(), sink, data, eris.NoTTL, eris.BlockOptions{})
	if err != nil {
		return fmt.Errorf("store block: %w", err)
	}
	if _, err := progress.PrintfStdout("%s\n", ref); err != nil {
		return fmt.Errorf("write hash to stdout: %w", err)
	}
	return nil
}

// blockGet parses and fetches one group at a time. A malformed hash ends the
// group, but the valid hashes before it still get their turn.
func blockGet(progress *zpgo.Progress, source eris.BlockSource, hashes []string, batchSize int) error {
	limit := source.BatchSize()
	switch {
	case batchSize == 0 || limit < 0:
		batchSize = batch.Limit(limit)
	case limit > 0:
		batchSize = min(batchSize, limit)
	}
	ctx := context.Background()
	requests := make([]eris.BlockRequest, 0, min(batchSize, len(hashes)))
	for group := range slices.Chunk(hashes, batchSize) {
		requests = requests[:0]
		var parseErr error
		for _, hash := range group {
			ref, err := eris.ParseReference(hash)
			if err != nil {
				parseErr = fmt.Errorf("invalid block hash %q: %w", hash, err)
				break
			}
			requests = append(requests, eris.BlockRequest{Reference: ref})
		}
		if len(requests) > 0 {
			if err := writeBlockBatch(ctx, progress, source, requests); err != nil {
				return err
			}
		}
		if parseErr != nil {
			return parseErr
		}
	}
	return nil
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
