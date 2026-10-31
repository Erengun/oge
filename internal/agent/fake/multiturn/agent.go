package multiturn

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// The fake agent's line protocol. Input, one line each: a user turn's
// text, or a marker. Output, tab-separated, one line each, ending every
// turn with an end line.
const (
	// MarkInterrupt ends the turn in flight early; idle, it is ignored.
	MarkInterrupt = "!interrupt"
	// MarkAllow and MarkDeny answer an ask.
	MarkAllow = "!allow"
	MarkDeny  = "!deny"

	// Output line kinds (the first field).
	KindClaim = "claim" // claim<TAB>text
	KindTool  = "tool"  // tool<TAB>name<TAB>target
	KindAsk   = "ask"   // ask<TAB>tool<TAB>target
	KindFile  = "file"  // file<TAB>path, after it was written
	KindUsage = "usage" // usage<TAB>cost<TAB>tokens
	KindEnd   = "end"   // end<TAB>done or end<TAB>interrupted
)

// End reasons on an end line.
const (
	EndDone        = "done"
	EndInterrupted = "interrupted"
)

// input reads turns and markers; lines that arrive while a turn is open
// queue, since nothing enters a running turn.
type input struct {
	r     *bufio.Reader
	queue []string
}

// next is the next line to treat as a turn, queued lines first.
func (in *input) next() (string, bool) {
	if len(in.queue) > 0 {
		l := in.queue[0]
		in.queue = in.queue[1:]
		return l, true
	}
	return in.read()
}

func (in *input) read() (string, bool) {
	l, err := in.r.ReadString('\n')
	if l == "" && err != nil {
		return "", false
	}
	return strings.TrimRight(l, "\r\n"), true
}

// until reads lines until one of the markers arrives and returns it;
// everything else queues. False means stdin closed first.
func (in *input) until(markers ...string) (string, bool) {
	for {
		l, ok := in.read()
		if !ok {
			return "", false
		}
		for _, m := range markers {
			if l == m {
				return l, true
			}
		}
		if !strings.HasPrefix(l, "!") {
			in.queue = append(in.queue, l)
		}
	}
}

// Serve plays sc as the fake agent: it reads user turns from r, writes
// events to w, and returns nil when r closes. dir is the Workspace.
func Serve(r io.Reader, w io.Writer, dir string, sc Scenario) error {
	in := &input{r: bufio.NewReaderSize(r, 1<<20)}
	out := func(fields ...string) { fmt.Fprintln(w, strings.Join(fields, "\t")) }
	for n := 0; ; {
		text, ok := in.next()
		if !ok {
			return nil
		}
		if text == "" || strings.HasPrefix(text, "!") {
			continue // a stray marker between turns
		}
		t := sc.Turn(n)
		n++
		if t.Reply != "" {
			out(KindClaim, oneLine(t.Reply))
		}
		for _, tl := range t.Tools {
			out(KindTool, tl.Name, oneLine(tl.Target))
		}
		allowed := true
		if t.Ask != nil {
			out(KindAsk, t.Ask.Tool, oneLine(t.Ask.Target))
			ans, ok := in.until(MarkAllow, MarkDeny)
			allowed = ok && ans == MarkAllow
		}
		if allowed {
			if err := WriteFiles(dir, t.Writes); err != nil {
				return err
			}
			for _, f := range t.Writes {
				out(KindFile, f.Path)
			}
		}
		if t.Hold {
			if _, ok := in.until(MarkInterrupt); ok {
				out(KindEnd, EndInterrupted)
				continue
			}
			out(KindEnd, EndInterrupted)
			return nil
		}
		if t.Usage != nil {
			out(KindUsage, strconv.FormatFloat(t.Usage.CostUSD, 'f', -1, 64), strconv.Itoa(t.Usage.Tokens), strconv.Itoa(t.Usage.OutTokens))
		}
		out(KindEnd, EndDone)
	}
}

func oneLine(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(s)
}
