package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	stdfs "io/fs"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const replHelp = `pwd                Print the current image directory.
cd [PATH]          Change directory. With no path, return to /.
ls, tree, stat, prefetch, resolve [PATH]
cat, readlink PATH
help, quit

ls and tree prefix each name with a type code.
  f file        d directory     l symlink       x executable
  p pipe        s socket        b block device  c character device
  u unknown
Use stat PATH for mode and size or readlink PATH for a symbolic link target.
Names with control characters, outer spaces, or an initial double quote use
double quotes. Enter the quoted name as shown, including escapes such as \n
for a newline.

/ starts at the image root. Relative paths start in the current directory.
. means the current directory. At the image root, .. stays at /.
`

// Keep the logical working directory in the session. Every lookup starts
// from the same image, including lookups after cd through a directory link.
type imageREPL struct {
	browser imageBrowser
	cwd     string // Absolute, slash-separated image path.
}

// displayImagePath leaves ordinary names readable. Quote names that would change
// the layout or lose characters when copied back into the prompt.
func displayImagePath(name string) string {
	if !utf8.ValidString(name) || strings.TrimSpace(name) != name || strings.HasPrefix(name, `"`) ||
		strings.IndexFunc(name, func(r rune) bool { return !strconv.IsPrint(r) }) >= 0 {
		return strconv.Quote(name)
	}
	return name
}

// A command takes the rest of its line as one path, including internal spaces.
// Diagnostics and prompts use stderr so that cat can emit arbitrary bytes.
func (b imageBrowser) repl(input io.Reader, interactive bool) error {
	shell := imageREPL{browser: b, cwd: "/"}
	reader := bufio.NewReader(input)
	for {
		if interactive {
			if _, err := b.p.PrintfStderr("%s> ", displayImagePath(shell.cwd)); err != nil {
				return err
			}
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return err
			}
			if line == "" {
				return nil
			}
		}
		line = strings.TrimSpace(line)
		op, name := line, ""
		if i := strings.IndexFunc(line, unicode.IsSpace); i >= 0 {
			op, name = line[:i], strings.TrimSpace(line[i:])
		}
		switch op {
		case "":
			continue
		case "quit", "exit":
			return nil
		case "help":
			if _, err := b.p.PrintStdout(replHelp); err != nil {
				return err
			}
			continue
		}
		if err := shell.execute(op, name); err != nil {
			if _, writeErr := b.p.PrintfStderr("fs: %s\n", displayImagePath(err.Error())); writeErr != nil {
				return writeErr
			}
		}
	}
}

// execute interprets shell paths before passing valid relative names to the
// image API. Cleaning an absolute image path clamps parent steps at its root.
func (s *imageREPL) execute(op, arg string) error {
	b := s.browser
	switch op {
	case "pwd", "cd", "ls", "tree", "stat", "prefetch", "resolve", "cat", "readlink":
	default:
		return fmt.Errorf("unknown command %q", op)
	}
	if op == "pwd" {
		if arg != "" {
			return errors.New("pwd takes no arguments")
		}
		_, err := b.p.PrintfStdout("%s\n", displayImagePath(s.cwd))
		return err
	}
	if strings.HasPrefix(arg, `"`) {
		name, err := strconv.Unquote(arg)
		if err != nil || name == "" {
			return fmt.Errorf("invalid quoted image path %q", arg)
		}
		arg = name
	}
	if arg == "" {
		switch op {
		case "cat", "readlink":
			return errors.New("expected an image path")
		case "cd":
			arg = "/"
		default:
			arg = "."
		}
	}
	base := s.cwd
	if path.IsAbs(arg) {
		base = "/"
	}
	absolute := path.Join(base, arg)
	name := strings.TrimPrefix(absolute, "/")
	if name == "" {
		name = "."
	}
	if !stdfs.ValidPath(name) {
		return fmt.Errorf("invalid image path %q", arg)
	}

	active, finish, err := b.meter.replActivity(b.p, op)
	if err != nil {
		return err
	}
	defer finish()
	b.p = active
	if op == "cd" {
		if _, err := b.view.Sub(b.ctx, name); err != nil {
			return fmt.Errorf("cd: cannot open directory %q: %w", arg, err)
		}
		s.cwd = absolute
		return nil
	}
	// A trailing slash, "." or ".." selects the directory itself, including
	// through a link. Keep the full image view for reads from other backends.
	_, last := path.Split(arg)
	if name != "." && (last == "" || last == "." || last == "..") {
		info, err := b.view.Stat(b.ctx, name)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", arg)
		}
		switch op {
		case "tree":
			return b.printTree(name, arg, stdfs.FileInfoToDirEntry(info))
		case "stat":
			return b.printStat(info)
		case "cat", "readlink":
			return fmt.Errorf("%s: %q is a directory", op, arg)
		}
	}
	return b.browse(op, name, arg)
}
