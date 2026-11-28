package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/zpgo"
)

type capabilityOptions struct {
	operation    string
	urns         []string
	blockOptions eris.BlockOptions
	blockSize    eris.BlockSize
	preserveRoot bool
	secretFile   string
	size, limit  int64
	ttl          eris.TTL
}

// parseCapCommand accepts storage flags at each command level. Operation
// flags follow the subcommand and precede any capability URNs.
func parseCapCommand(flags *flag.FlagSet, args []string) (commandOptions, error) {
	command := commandOptions{name: "cap", usage: capUsage}
	if err := flags.Parse(args); err != nil {
		return command, fmt.Errorf("cap: %w", err)
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		return command, flag.ErrHelp
	}
	usage, ok := capOperationUsage[args[0]]
	if !ok {
		return command, fmt.Errorf("cap: unknown subcommand %q", args[0])
	}
	command.name += " " + args[0]
	command.usage = usage
	options := &command.capability
	options.operation = args[0]

	var blockSize int
	var ttl uint
	if options.operation != "resolve-path" {
		flags.BoolVar(&options.blockOptions.HighPriority, "high-priority", false, "request priority handling")
		flags.BoolVar(&options.blockOptions.Root, "root", false, "set the root flag")
	}
	switch options.operation {
	case "encode":
		flags.IntVar(&blockSize, "block-size", 0, "ERIS block size in bytes")
		flags.Func("secret-file", "file with the 32-byte convergence secret", func(path string) error {
			if path == "" {
				return errors.New("expected a path")
			}
			options.secretFile = path
			return nil
		})
	case "decode":
		flags.Int64Var(&options.size, "size", -1, "known decoded size in bytes")
		flags.Int64Var(&options.limit, "limit", -1, "maximum decoded size in bytes")
		fallthrough
	case "prefetch":
		flags.UintVar(&ttl, "ttl", 0, "network forwarding budget")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return command, fmt.Errorf("%s: %w", command.name, err)
	}
	if ttl > math.MaxUint8 {
		return command, fmt.Errorf("%s: --ttl must be between 0 and %d", command.name, math.MaxUint8)
	}
	options.ttl = eris.TTL(ttl)
	options.urns = flags.Args()
	switch options.operation {
	case "encode":
		if flags.NArg() != 0 {
			return command, fmt.Errorf("cap encode: unexpected arguments: %q", options.urns)
		}
		if blockSize != 0 && !eris.ValidBlockLength(blockSize) {
			return command, fmt.Errorf("cap encode: --block-size must be 0, %d, or %d", eris.BlockSize1KiB, eris.BlockSize32KiB)
		}
		options.blockSize = eris.BlockSize(blockSize)
		// An explicit --root value applies to every block. When omitted,
		// let the encoder mark just the root block.
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "root" {
				options.preserveRoot = true
			}
		})
	case "decode":
		if flags.NArg() != 1 {
			return command, errors.New("cap decode: expected exactly one capability URN")
		}
		if options.size < -1 {
			return command, errors.New("cap decode: --size must be at least -1")
		}
		if options.limit < -1 {
			return command, errors.New("cap decode: --limit must be at least -1")
		}
		if options.size >= 0 && options.ttl != eris.NoTTL {
			return command, errors.New("cap decode: --size requires the default TTL")
		}
	default:
		if flags.NArg() == 0 {
			return command, fmt.Errorf("%s: expected at least one capability URN", command.name)
		}
	}
	return command, nil
}

// run uses the configured capability store for encoding, reading, and
// lookups. Operations with several URNs process them in argument order.
func (options capabilityOptions) run(progress *zpgo.Progress, store eris.CapabilityStore, input io.Reader) error {
	ctx := context.Background()
	if options.operation == "encode" {
		return options.encode(ctx, progress, store, input)
	}
	for _, urn := range options.urns {
		capability, err := eris.ParseURN(urn)
		if err != nil {
			return fmt.Errorf("invalid capability URN %q: %w", urn, err)
		}
		switch options.operation {
		case "decode":
			return options.decode(ctx, progress, store, capability)
		case "prefetch":
			if err := store.Prefetch(ctx, capability, options.ttl, options.blockOptions); err != nil {
				return fmt.Errorf("prefetch capability %s: %w", capability, err)
			}
		case "resolve-path":
			path, err := store.ResolvePath(ctx, capability)
			if err != nil {
				return fmt.Errorf("resolve path for capability %s: %w", capability, err)
			}
			if _, err := progress.PrintfStdout("%s\n", path); err != nil {
				return fmt.Errorf("write path to stdout: %w", err)
			}
		}
	}
	return nil
}

// encode streams stdin into the store and prints the URN after a successful write.
func (options capabilityOptions) encode(ctx context.Context, progress *zpgo.Progress, store eris.CapabilityStore, input io.Reader) error {
	secret, err := readSecret(options.secretFile)
	if err != nil {
		return fmt.Errorf("read convergence secret: %w", err)
	}
	capability, err := store.EncodeContentWithOptions(ctx, input, secret, eris.EncodeOptions{
		BlockOptions: options.blockOptions,
		BlockSize:    options.blockSize,
		PreserveRoot: options.preserveRoot,
	})
	if err != nil {
		return fmt.Errorf("encode stdin: %w", err)
	}
	if _, err := progress.PrintfStdout("%s\n", capability); err != nil {
		return fmt.Errorf("write capability URN to stdout: %w", err)
	}
	return nil
}

// readSecret uses the null secret when no path is set. Read one byte beyond
// the required length to detect excess data without buffering the whole file.
func readSecret(path string) (eris.Secret, error) {
	var secret eris.Secret
	if path == "" {
		return secret, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return secret, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, int64(len(secret))+1))
	if err != nil {
		return secret, err
	}
	if len(data) != len(secret) {
		return secret, fmt.Errorf("secret file must contain exactly %d bytes", len(secret))
	}
	copy(secret[:], data)
	return secret, nil
}

// decode copies one content stream to stdout and closes its reader before
// the storage chain closes.
func (options capabilityOptions) decode(ctx context.Context, progress *zpgo.Progress, store eris.CapabilityStore, capability eris.Capability) (err error) {
	var reader eris.CapabilityReader
	if options.size >= 0 {
		if options.limit >= 0 && options.size > options.limit {
			return fmt.Errorf("declared size %d exceeds --limit %d: %w", options.size, options.limit, eris.ErrDecodedTooLarge)
		}
		reader, err = store.NewReaderWithSize(ctx, capability, options.size, options.blockOptions)
	} else {
		reader, err = store.NewReaderWithLimit(ctx, capability, options.limit, options.ttl, options.blockOptions)
	}
	if err != nil {
		return fmt.Errorf("open decoded content: %w", err)
	}
	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close content reader: %w", closeErr))
		}
	}()
	if _, err := io.Copy(stdoutWriter{progress}, reader); err != nil {
		return fmt.Errorf("copy decoded content to stdout: %w", err)
	}
	return nil
}

// stdoutWriter connects io.Copy to the progress reporter's stdout method.
type stdoutWriter struct{ *zpgo.Progress }

func (w stdoutWriter) Write(data []byte) (int, error) { return w.WriteStdout(data) }
