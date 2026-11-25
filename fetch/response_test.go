package fetch

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

const helloCap = "urn:eris:BIAD77QDJMFAKZYH2DXBUZYAP3MXZ3DJZVFYQ5DFWC6T65WSFCU5S2IT4YZGJ7AC4SYQMP2DM2ANS2ZTCP3DJJIRV733CRAAHOSWIYZM3M"
const okFrame = "eris-fetch/1 ok\nkind blob\ncap " + helloCap + "\n"
const gitShared = "type git\nurl https://example.org/project.git\n"
const commit = "commit sha1:1111111111111111111111111111111111111111\n"
const refinement = "eris-fetch/1 refine\n" + gitShared + "@default tag r/%FF\n" + commit + "@alt tip main\n" + commit + "@end\n"

func TestResponses(t *testing.T) {
	for _, wire := range []string{okFrame, "eris-fetch/1 fail\ncode x-unknown\n", "eris-fetch/1 fail\ncode unknown-key\nkey a.b\n", refinement} {
		r, err := ParseResponse(iotest.OneByteReader(strings.NewReader(wire)))
		if err != nil {
			t.Fatal(err)
		}
		b, err := r.MarshalText()
		if err != nil || string(b) != wire {
			t.Error(string(b), err)
		}
		request := opening
		if r.Status == "refine" {
			request = gitShared + "branch main\n"
			if r.Choices[0].Label != "r/\xff" {
				t.Fatal("label")
			}
			for i := range r.Choices {
				s, err := r.Proposal(i)
				if err != nil || s.Canonical() != commit+gitShared {
					t.Fatal(s, err)
				}
			}
		}
		if err := r.ValidateFor(source(t, request), false); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Proposal(-1); err == nil {
			t.Fatal("bad index")
		}
	}
}

func TestSelectGitTip(t *testing.T) {
	request := source(t, gitShared+"branch main\nsubdir pkg/tool\n")
	shared := "subdir pkg/tool\n" + gitShared
	tip := "commit sha1:" + strings.Repeat("2", 40) + "\n"
	wire := "eris-fetch/1 refine\n" + shared + "@default tag v1\n" + commit + "@alt tip main\n" + tip + "@end\n"
	choices, err := ParseResponse(strings.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if err := choices.ValidateFor(request, false); err != nil {
		t.Fatal(err)
	}
	selected, err := choices.Proposal(1)
	if err != nil || selected.Canonical() != tip+shared {
		t.Fatalf("selected source %q, error %v", selected.Canonical(), err)
	}

	result, err := ParseResponse(strings.NewReader("eris-fetch/1 ok\nkind erisfs\ncap " + emptyTreeCap + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := result.ValidateFor(selected, false); err != nil {
		t.Fatal(err)
	}
	if err := result.ValidateFor(request, false); !errors.Is(err, ErrProtocol) {
		t.Fatalf("success for unresolved request: %v", err)
	}
}

func TestMalformedResponse(t *testing.T) {
	h := "eris-fetch/1 refine\n"
	for _, wire := range []string{"", "eris-fetch/1 unknown\n", "eris-fetch/1 ok\n", "eris-fetch/1 fail\n", "eris-fetch/1 fail\ncode CAPS\n", strings.TrimSuffix(okFrame, "\n"), okFrame + "x\n", strings.ReplaceAll(okFrame, "\n", "\r\n"), strings.Replace(okFrame, "blob", "Blob", 1), strings.Replace(okFrame, "YZM3M", "YZM3N", 1),
		h + "@end\n", h + "type x\n@default x y\n", h + "type x\n@default x y\n@end\n#\n",
		h + "type x\n@default x y\n@default x z\n@end\n", h + "type x\n@default x y\n@alt x y\n@end\n",
		h + "type x\n@default x y\ntype x\n@end\n", h + "@default x y\n@end\n",
		h + "a.b\ntype x\n@default x y\na\n@end\n", h + "a\ntype x\n@default x y\na.b\n@end\n",
		h + "a\na-b\na.b\ntype x\n@default x y\n@end\n", h + "type x\n@default x y\na\na.b\n@end\n",
		h + "z\ntype x\n@default x y\n@end\n", h + "type X\n@default x y\n@end\n", h + "a \ntype x\n@default x y\n@end\n",
	} {
		if _, err := ParseResponse(strings.NewReader(wire)); !errors.Is(err, ErrProtocol) {
			t.Errorf("accepted %q", wire)
		}
	}
	for _, label := range []string{"a%", "%ff", "%7A", "raw space", "", "ä", "@", "a%GG"} {
		if _, err := ParseResponse(strings.NewReader(strings.Replace(refinement, "r/%FF", label, 1))); err == nil {
			t.Fatal(label)
		}
	}
	if _, err := ParseResponse(io.MultiReader(strings.NewReader(okFrame), iotest.ErrReader(io.ErrUnexpectedEOF))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
}

func TestValidateFor(t *testing.T) {
	for _, tc := range []struct{ request, wire string }{
		{gitShared, okFrame}, {opening, refinement}, {opening, strings.Replace(okFrame, "blob", "erisfs", 1)},
		{"type unknown", okFrame}, {gitShared + "branch dev", refinement}, {gitShared, strings.Replace(refinement, ".org", ".net", 1)},
		{gitShared, strings.Replace(refinement, "@default tag", "@default tip", 1)}, {gitShared, strings.Replace(refinement, "@alt tip", "@alt tag", 1)},
		{gitShared, strings.Replace(refinement, "r/%FF", "x..y", 1)}, {gitShared, strings.Replace(refinement, commit, "commit bad\n", 1)},
		{gitShared, strings.Replace(refinement, commit, "commit sha256:"+strings.Repeat("0", 64)+"\n", 1)},
	} {
		r, err := ParseResponse(strings.NewReader(tc.wire))
		if err != nil {
			t.Fatal(err)
		}
		if r.ValidateFor(source(t, tc.request), true) == nil {
			t.Errorf("accepted %q for %q", tc.wire, tc.request)
		}
	}
}

func TestInvalidMarshal(t *testing.T) {
	for _, r := range []Response{{}, {Status: "ok", Kind: "blob"}, {Status: "fail"}, {Status: "fail", Failure: Failure{"x", "a."}}, {Status: "refine"},
		{Status: "refine", Shared: strings.Repeat(" ", MaxSourceSize+1)},
		{Status: "refine", Choices: []Choice{{Kind: "X"}}},
		{Status: "refine", Choices: []Choice{{Kind: "x", Label: strings.Repeat("%", MaxMarkerSize/3+1)}}},
		{Status: "refine", Shared: gitShared, Choices: []Choice{{"tag", "a", commit + "@alt tip main\n" + commit}}},
	} {
		if _, err := r.MarshalText(); err == nil {
			t.Errorf("accepted malformed %s", r.Status)
		}
	}
}

func TestFrameLimits(t *testing.T) {
	h := "eris-fetch/1 refine\n"
	record := "a " + strings.Repeat("x", MaxLineSize-2) + "\n"
	for _, wire := range []string{
		h + strings.Repeat(record, 16) + "type x\n@default x y\n@end\n",
		h + strings.Repeat(record, 15) + "type x\n@default x y\n" + strings.Replace(record, "a ", "b ", 1) + "@end\n",
		h + "type x\n@default tag " + strings.Repeat("x", MaxMarkerSize) + "\n@end\n",
	} {
		if _, err := ParseResponse(strings.NewReader(wire)); err == nil {
			t.Fatal("over limit")
		}
	}
	// Reuse one long record to exercise the 64 MiB ceiling without storing a
	// second copy of the entire fixture in the test.
	var readers []io.Reader
	readers = append(readers, strings.NewReader(h+"type x\n"))
	body := []byte(strings.Repeat(strings.Replace(record, "a ", "b ", 1), 15))
	for i := range 70 {
		marker := "@alt"
		if i == 0 {
			marker = "@default"
		}
		readers = append(readers, strings.NewReader(marker+" tag "+strings.Repeat("z", i+1)+"\n"), bytes.NewReader(body))
	}
	readers = append(readers, strings.NewReader("@end\n"))
	var failure Failure
	if _, err := ParseResponse(io.MultiReader(readers...)); !errors.Is(err, ErrProtocol) || !errors.Is(err, errScanLimit) || errors.As(err, &failure) {
		t.Fatal(err)
	}
}

func TestExactRefinementLimits(t *testing.T) {
	const header = "eris-fetch/1 refine\n"
	const marker = "@default x "
	const typ = "type x\n"
	record := "a " + strings.Repeat("x", MaxLineSize-2) + "\n"
	shared := strings.Repeat(record, 15) + typ
	label := strings.Repeat("x", MaxMarkerSize-len(marker))
	choiceSize := MaxSourceSize - len(shared)
	choice := "z " + strings.Repeat("y", choiceSize-len("z \n")) + "\n"
	wire := header + shared + marker + label + "\n" + choice + "@end\n"

	r, err := ParseResponse(strings.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Proposal(0)
	if err != nil || s.Canonical() != shared+choice {
		t.Fatalf("assemble source at the byte ceiling: %v", err)
	}
	b, err := r.MarshalText()
	if err != nil || string(b) != wire {
		t.Fatalf("marshal response at both ceilings: %v", err)
	}
}

func TestMaximumFailureKey(t *testing.T) {
	key := strings.Repeat("k", MaxLineSize)
	wire := "eris-fetch/1 fail\ncode unknown-key\nkey " + key + "\n"
	r, err := ParseResponse(strings.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.MarshalText()
	if err != nil || string(b) != wire {
		t.Fatal(err)
	}
}
