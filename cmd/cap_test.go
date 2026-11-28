package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/asciimoth/goeris"
	"github.com/asciimoth/goeris/volume"
	"github.com/asciimoth/zpgo"
)

func TestCapRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name     string
		length   int
		expected eris.BlockSize
		encode   []string
		decode   []string
	}{
		{"empty", 0, eris.BlockSize1KiB, nil, []string{"--size=0"}},
		{"one full block", 1024, eris.BlockSize1KiB, nil, []string{"--limit=1024"}},
		{"below threshold", 16383, eris.BlockSize1KiB, nil, nil},
		{"at threshold", 16384, eris.BlockSize32KiB, nil, nil},
		{"small blocks at multiple levels", 32768, eris.BlockSize1KiB, []string{"--block-size=1024"}, []string{"--size=32768", "--limit=32768"}},
		{"large blocks", 100001, eris.BlockSize32KiB, []string{"--block-size=32768"}, []string{"--ttl=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			files := t.TempDir()
			inputFile := filepath.Join(files, "input")
			urnFile := filepath.Join(files, "urn")
			outputFile := filepath.Join(files, "output")
			data := make([]byte, tc.length)
			// Include binary bytes and vary the pattern between blocks.
			for i := range data {
				data[i] = byte(i ^ i>>8 ^ i>>16)
			}
			if err := os.WriteFile(inputFile, data, 0o600); err != nil {
				t.Fatal(err)
			}
			encodeArgs := append([]string{"--replace-blocks", root, "cap", "encode"}, tc.encode...)
			urn := runCapProcess(t, inputFile, urnFile, encodeArgs...)
			capability := parseTestCapability(t, string(urn))
			if capability.BlockSize != tc.expected {
				t.Fatalf("block size = %d, want %d", capability.BlockSize, tc.expected)
			}
			decodeArgs := append([]string{"cap", "--replace-blocks", root, "decode"}, tc.decode...)
			out := runCapProcess(t, "", outputFile, append(decodeArgs, capability.String())...)
			if !bytes.Equal(out, data) {
				t.Fatalf("decoded bytes differ from input (got %d bytes, want %d)", len(out), len(data))
			}
		})
	}
}

const capProcessEnv = "GOERIS_TEST_CAP_PROCESS"

// TestCapCommandProcess runs the normal command entry point in a child process.
func TestCapCommandProcess(t *testing.T) {
	if os.Getenv(capProcessEnv) != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator == -1 {
		t.Fatal("missing command argument separator")
	}
	os.Exit(run(os.Args[separator+1:]))
}

// runCapProcess gives the child real file descriptors for CLI input and output.
func runCapProcess(t *testing.T, inputPath, outputPath string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestCapCommandProcess$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), capProcessEnv+"=1", "ERIS_BLOCK_PATH=;", "ZIG_PROGRESS=")
	if inputPath != "" {
		input, err := os.Open(inputPath)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = input.Close() }()
		cmd.Stdin = input
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = createOutputFile(t, outputPath)
	if err := cmd.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("%v: %v, stderr = %q", args, err, &stderr)
	}
	out, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCapArguments(t *testing.T) {
	urn := (eris.Capability{BlockSize: eris.BlockSize1KiB}).String()
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{"group without arguments", nil, 0, capUsage, ""},
		{"group help", []string{"--help"}, 0, capUsage, ""},
		{"group help command", []string{"help"}, 0, capUsage, ""},
		{"encode help", []string{"encode", "--help"}, 0, capEncodeUsage, ""},
		{"decode help", []string{"decode", "--size=0", "--help"}, 0, capDecodeUsage, ""},
		{"prefetch help", []string{"prefetch", "--help"}, 0, capPrefetchUsage, ""},
		{"resolve-path help", []string{"resolve-path", "--help"}, 0, capResolvePathUsage, ""},
		{"unknown subcommand", []string{"wrong"}, 2, "", `unknown subcommand "wrong"`},
		{"encode argument", []string{"encode", urn}, 2, "", "unexpected arguments"},
		{"invalid block size", []string{"encode", "--block-size=2048"}, 2, "", "--block-size must be"},
		{"empty secret path", []string{"encode", "--secret-file="}, 2, "", "expected a path"},
		{"missing decode URN", []string{"decode"}, 2, "", "expected exactly one capability URN"},
		{"too many decode URNs", []string{"decode", urn, urn}, 2, "", "expected exactly one capability URN"},
		{"decode batch flag", []string{"decode", "--batch-size=1", urn}, 2, "", "flag provided but not defined"},
		{"invalid limit", []string{"decode", "--limit=-2", urn}, 2, "", "--limit must be at least -1"},
		{"invalid size", []string{"decode", "--size=-2", urn}, 2, "", "--size must be at least -1"},
		{"size and TTL", []string{"decode", "--size=0", "--ttl=1", urn}, 2, "", "--size requires the default TTL"},
		{"invalid TTL", []string{"prefetch", "--ttl=256", urn}, 2, "", "--ttl must be between 0 and 255"},
		{"missing prefetch URN", []string{"prefetch"}, 2, "", "expected at least one capability URN"},
		{"missing resolve-path URN", []string{"resolve-path"}, 2, "", "expected at least one capability URN"},
		{"invalid decode URN", []string{"--replace-blocks=", "decode", "invalid"}, 1, "", "invalid capability URN"},
		{"invalid prefetch URN", []string{"prefetch", "--replace-blocks=", "invalid"}, 1, "", "invalid capability URN"},
		{"invalid resolve-path URN", []string{"resolve-path", "--replace-blocks=", "invalid"}, 1, "", "invalid capability URN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ERIS_BLOCK_PATH", ";")
			code, out, stderr := runCommand(t, nil, append([]string{"cap"}, tc.args...)...)
			if code != tc.code || out != tc.stdout || tc.stderr == "" && stderr != "" || !strings.Contains(stderr, tc.stderr) {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q", code, out, stderr)
			}
		})
	}
}

func TestCapStoreErrors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ERIS_BLOCK_PATH", root)
	t.Setenv("ERIS_MNT", t.TempDir())

	data := bytes.Repeat([]byte{0, 128, 255}, 850)
	capability := encodeTestContent(t, data, "cap", "encode")
	urn := capability.String()
	missing := eris.Capability{BlockSize: eris.BlockSize1KiB, Level: 1}.String()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing read", []string{"decode", missing}, "open decoded content"},
		{"missing prefetch index", []string{"prefetch", urn, missing}, "prefetch capability " + missing},
		{"missing mount path", []string{"resolve-path", urn}, "resolve path for capability " + urn},
		{"no capability stores", []string{"decode", "--replace-capabilities=", urn}, eris.ErrNoCapabilityStorage.Error()},
		{"read-only mounts", []string{"encode", "--replace-blocks="}, eris.ErrReadOnly.Error()},
		{"declared size above limit", []string{"decode", "--size=2550", "--limit=2549", urn}, eris.ErrDecodedTooLarge.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, stderr := runCommand(t, []byte{}, append([]string{"cap"}, tc.args...)...)
			if code != 1 || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit code = %d, stdout = %q, stderr = %q, want %v", code, out, stderr, tc.want)
			}
		})
	}

	// Delete the middle leaf. The reader can open using the root and final
	// leaf. The copy must write the first leaf and report the missing one.
	rootBlock, err := os.ReadFile(filepath.Join(root, volume.BlockPath(capability.Root.Reference)))
	if err != nil {
		t.Fatal(err)
	}
	node, err := eris.DecryptNode(rootBlock, capability.Root, capability.Level)
	if err != nil {
		t.Fatal(err)
	}
	leaves, err := eris.ParseInternalNode(node)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, volume.BlockPath(leaves[1].Reference))); err != nil {
		t.Fatal(err)
	}
	code, out, stderr := runCommand(t, nil, "cap", "decode", urn)
	if code != 1 || out != string(data[:eris.BlockSize1KiB]) || !strings.Contains(stderr, "copy decoded content") ||
		!strings.Contains(stderr, eris.ErrMissingBlock.Error()) {
		t.Fatalf("decode exit code = %d, stdout = %d bytes, stderr = %q", code, len(out), stderr)
	}
}

func TestCapMount(t *testing.T) {
	first := eris.Capability{BlockSize: eris.BlockSize1KiB}
	second := first
	second.Root.Reference[0] = 1
	if _, err := filepath.Localize(first.String()); err != nil {
		t.Skipf("host cannot represent capability file names: %v", err)
	}
	root := t.TempDir()
	mountArgs := []string{"cap", "--replace-blocks=", "--replace-capabilities", root}
	firstPath := filepath.Join(root, first.String())
	secondPath := filepath.Join(root, second.String())
	data := []byte{0, 0xff, '\r', '\n'}
	for _, path := range []string{firstPath, secondPath} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		args   []string
		stdout string
	}{
		{[]string{"decode", first.String()}, string(data)},
		{[]string{"resolve-path", second.String(), first.String()}, secondPath + "\n" + firstPath + "\n"},
		{[]string{"prefetch", "--root", "--high-priority", "--ttl=255", first.String(), second.String()}, ""},
	} {
		code, out, stderr := runCommand(t, nil, append(mountArgs, tc.args...)...)
		if code != 0 || out != tc.stdout || stderr != "" {
			t.Errorf("%v: exit code = %d, stdout = %q, stderr = %q", tc.args, code, out, stderr)
		}
	}
	for _, tc := range []struct {
		option string
		want   error
	}{
		{"--size=3", eris.ErrSizeMismatch},
		{"--limit=3", eris.ErrDecodedTooLarge},
	} {
		code, out, stderr := runCommand(t, nil, append(mountArgs, "decode", tc.option, first.String())...)
		if code != 1 || out != "" || !strings.Contains(stderr, tc.want.Error()) {
			t.Errorf("%s: exit code = %d, stdout = %q, stderr = %q, want %v", tc.option, code, out, stderr, tc.want)
		}
	}

	// The default chain starts with blocks, then tries the mount.
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	t.Setenv("ERIS_MNT", root)
	for _, options := range [][]string{nil, {"--size=4"}} {
		args := append([]string{"cap", "decode"}, options...)
		code, out, stderr := runCommand(t, nil, append(args, first.String())...)
		if code != 0 || out != string(data) || stderr != "" {
			t.Errorf("fallback with %v: exit code = %d, stdout = %q, stderr = %q", options, code, out, stderr)
		}
	}
}

func TestCapSecretFile(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	path := filepath.Join(t.TempDir(), "secret")
	code, out, stderr := runCommand(t, []byte{}, "cap", "encode", "--secret-file", path)
	if code != 1 || out != "" || !strings.Contains(stderr, "read convergence secret") {
		t.Errorf("missing secret file: exit code = %d, stdout = %q, stderr = %q", code, out, stderr)
	}
	for _, length := range []int{eris.KeySize - 1, eris.KeySize + 1} {
		if err := os.WriteFile(path, make([]byte, length), 0o600); err != nil {
			t.Fatal(err)
		}
		code, out, stderr := runCommand(t, []byte{}, "cap", "encode", "--secret-file", path)
		if code != 1 || out != "" || !strings.Contains(stderr, fmt.Sprintf("exactly %d bytes", eris.KeySize)) {
			t.Errorf("secret length %d: exit code = %d, stdout = %q, stderr = %q", length, code, out, stderr)
		}
	}
	secret := eris.Secret{1, 2, 3}
	if err := os.WriteFile(path, secret[:], 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("content encoded with a convergence secret")
	capability := encodeTestContent(t, data, "cap", "encode", "--secret-file", path)
	// One leaf is enough to verify that the CLI passes the secret to ERIS.
	node := make([]byte, eris.BlockSize1KiB)
	copy(node, data)
	node[len(data)] = 0x80
	_, pair, err := eris.EncryptNode(node, secret, 0)
	if err != nil {
		t.Fatal(err)
	}
	if capability.Root != pair {
		t.Fatal("encoded root does not match the root for the supplied secret")
	}
	code, out, stderr = runCommand(t, nil, "cap", "decode", capability.String())
	if code != 0 || out != string(data) || stderr != "" {
		t.Fatalf("decode exit code = %d, stdout = %q, stderr = %q", code, out, stderr)
	}
}

func TestCapBlockOptions(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		root bool
		leaf bool
	}{
		{"mark only the root by default", nil, true, false},
		{"set root on every block", []string{"--root"}, true, true},
		{"clear root on every block", []string{"--root=false"}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _ := captureOutput(t)
			command, _, err := parseCommand(append([]string{"cap", "encode", "--high-priority"}, tc.args...))
			if err != nil {
				t.Fatal(err)
			}
			blocks := eris.BlockSet{Blocks: make(map[eris.Reference]eris.Block)}
			store := eris.NewBlockCapabilityStore(blocks)
			var progress zpgo.Progress
			if err := command.capability.run(&progress, store, strings.NewReader(strings.Repeat("a", 1025))); err != nil {
				t.Fatal(err)
			}
			urn := strings.TrimSuffix(readOutput(t, stdout), "\n")
			capability, err := eris.ParseURN(urn)
			if err != nil || capability.Level != 1 {
				t.Fatalf("encoded capability = %q, error = %v, want a two-level tree", urn, err)
			}
			for ref, block := range blocks.Blocks {
				want := tc.leaf
				if ref == capability.Root.Reference {
					want = tc.root
				}
				if block.Options.Root != want || !block.Options.HighPriority {
					t.Errorf("block %s options = %+v, want root=%t and high-priority=true", ref, block.Options, want)
				}
			}
		})
	}
}

func TestCapPrefetchOptions(t *testing.T) {
	first := eris.Capability{BlockSize: eris.BlockSize1KiB}
	second := first
	second.Root.Reference[0] = 1
	stdout, _ := captureOutput(t)
	command, _, err := parseCommand([]string{"cap", "prefetch", "--ttl=5", "--high-priority", "--root", first.String(), second.String()})
	if err != nil {
		t.Fatal(err)
	}
	var blocks recordingPrefetchStore
	var progress zpgo.Progress
	if err := command.capability.run(&progress, eris.NewBlockCapabilityStore(&blocks), nil); err != nil {
		t.Fatal(err)
	}
	if out := readOutput(t, stdout); out != "" {
		t.Errorf("prefetch stdout = %q, want empty output", out)
	}
	if len(blocks.requests) != 2 {
		t.Fatalf("prefetch requests = %v, want two separate requests", blocks.requests)
	}
	for i, capability := range []eris.Capability{first, second} {
		want := eris.BlockRequest{
			Reference: capability.Root.Reference,
			TTL:       5,
			Options:   eris.BlockOptions{HighPriority: true, Root: true},
		}
		if got := blocks.requests[i]; len(got) != 1 || got[0] != want {
			t.Errorf("prefetch request %d = %v, want [%+v]", i, got, want)
		}
	}
}

func TestCapOutputFailure(t *testing.T) {
	t.Setenv("ERIS_BLOCK_PATH", t.TempDir())
	data := []byte{0, 128, 255}
	capability := encodeTestContent(t, data, "cap", "encode")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"encode"}, "write capability URN to stdout"},
		{[]string{"decode", capability.String()}, "copy decoded content to stdout"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			stdout, stderr := captureOutput(t)
			if err := stdout.Close(); err != nil {
				t.Fatal(err)
			}
			input := createOutputFile(t, filepath.Join(t.TempDir(), "stdin"))
			// The encode command can read an empty byte stream from this file.
			original := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = original }()
			code := run(append([]string{"cap"}, tc.args...))
			if errout := readOutput(t, stderr); code != 1 || !strings.Contains(errout, tc.want) {
				t.Errorf("exit code = %d, stderr = %q", code, errout)
			}
		})
	}
}

// encodeTestContent runs one encode and checks the exact URN line on stdout.
func encodeTestContent(t *testing.T, data []byte, args ...string) eris.Capability {
	t.Helper()
	code, out, stderr := runCommand(t, data, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("encode exit code = %d, stdout = %q, stderr = %q", code, out, stderr)
	}
	return parseTestCapability(t, out)
}

// parseTestCapability checks that encode printed one canonical URN line.
func parseTestCapability(t *testing.T, out string) eris.Capability {
	t.Helper()
	capability, err := eris.ParseURN(strings.TrimSuffix(out, "\n"))
	if err != nil || out != capability.String()+"\n" {
		t.Fatalf("encode stdout = %q, want one canonical URN line: %v", out, err)
	}
	return capability
}
