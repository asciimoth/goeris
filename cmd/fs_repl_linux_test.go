package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// A terminal is needed here because redirected stderr disables the renderer.
func replTerminal(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Skipf("open pseudo-terminal: %v", err)
	}
	master := os.NewFile(uintptr(fd), "pty master")
	t.Cleanup(func() { _ = master.Close() })
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetUint32(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func TestREPLTerminalQuietAtPrompt(t *testing.T) {
	imageTestVolume(t)
	data := bytes.Repeat([]byte("x"), 2<<20)
	root := t.TempDir()
	dir := filepath.Join(root, "user docs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "large")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	urn := strings.TrimSpace(imageCommand(t, "build", root))
	master, slave := replTerminal(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCapCommandProcess$", "--", "fs", "repl", urn)
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(s string) bool {
		return strings.HasPrefix(s, "ZIG_PROGRESS=") || strings.HasPrefix(s, "ZPG_NO_PROGRESS=")
	}), capProcessEnv+"=1")
	cmd.Stdin, cmd.Stderr = slave, slave
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	terminal := bufio.NewReader(master)
	until := func(marker string) string {
		t.Helper()
		if err := master.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
			t.Fatal(err)
		}
		var seen strings.Builder
		for !strings.HasSuffix(seen.String(), marker) {
			c, err := terminal.ReadByte()
			if err != nil {
				t.Fatalf("wait for %q: %v, output %q", marker, err, seen.String())
			}
			seen.WriteByte(c)
		}
		return seen.String()
	}
	quiet := func() {
		t.Helper()
		if err := master.SetReadDeadline(time.Now().Add(1200 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if c, err := terminal.ReadByte(); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("unexpected terminal output after prompt: %q, %v", c, err)
		}
	}
	enter := func(line string) {
		t.Helper()
		if _, err := io.WriteString(master, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	if got := until("> "); got != "/> " {
		t.Fatalf("initial display = %q", got)
	}
	quiet()
	enter("cd user docs")
	if got := until("> "); got != "cd user docs\r\n/user docs> " {
		t.Fatalf("prompt after cd = %q", got)
	}
	enter("ls")
	if got := until("> "); got != "ls\r\n/user docs> " {
		t.Fatalf("short command display = %q", got)
	}
	out := bufio.NewReader(stdout)
	if line, err := out.ReadString('\n'); err != nil || line != "f large\n" {
		t.Fatalf("ls = %q, %v", line, err)
	}
	// Leave stdout full until cat has reported progress, then let it finish.
	enter("cat large")
	until("Reading KiB: user docs/large")
	got := make([]byte, len(data))
	if _, err := io.ReadFull(out, got); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("cat output differs: %v", err)
	}
	until("/user docs> ")
	quiet()
	enter("cd ..")
	if got := until("> "); got != "cd ..\r\n/> " {
		t.Fatalf("prompt after cd .. = %q", got)
	}
	enter("quit")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
