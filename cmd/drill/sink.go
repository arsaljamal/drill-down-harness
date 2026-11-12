package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/arsaljamal/drill-down-harness/viewer"
)

// sink sends each run event to every place that wants it: stderr as JSON
// (for programs), a recording file (for replays) and the live viewer.
type sink struct {
	json bool
	file *os.File
	hub  *viewer.Hub
	srv  *http.Server
	url  string
}

func newSink(asJSON bool, record, uiAddr string) (*sink, error) {
	s := &sink{json: asJSON}
	if record != "" {
		f, err := os.Create(record)
		if err != nil {
			return nil, fmt.Errorf("record: %w", err)
		}
		s.file = f
	}
	if uiAddr != "" {
		if err := s.serve(uiAddr); err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "watch it live: %s\n", s.url)
	}
	return s, nil
}

func (s *sink) serve(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	s.hub = viewer.NewHub()
	s.srv = &http.Server{Handler: s.hub.Handler(), ReadHeaderTimeout: 5 * time.Second}
	s.url = "http://" + ln.Addr().String()
	go s.srv.Serve(ln)
	return nil
}

func (s *sink) emit(ev map[string]any) {
	if _, ok := ev["at"]; !ok {
		ev["at"] = time.Now().UnixMilli()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.publish(b)
}

func (s *sink) publish(b []byte) {
	if s.json {
		fmt.Fprintln(os.Stderr, string(b))
	}
	if s.file != nil {
		s.file.Write(append(b, '\n'))
	}
	if s.hub != nil {
		s.hub.Publish(b)
	}
}

// close finishes the stream. With a viewer open, the page stays up until
// Ctrl-C so the result can be read.
func (s *sink) close() {
	if s.file != nil {
		s.file.Close()
	}
	if s.hub == nil {
		return
	}
	s.hub.Close()
	fmt.Fprintf(os.Stderr, "finished; the viewer stays open at %s (Ctrl-C to exit)\n", s.url)
	wait := make(chan os.Signal, 1)
	signal.Notify(wait, os.Interrupt)
	<-wait
	s.srv.Close()
}

// replay plays a recorded run back in the viewer, at its original pace
// divided by --speed (long pauses are capped so replays stay watchable).
func replay(args []string) int {
	fset := flag.NewFlagSet("replay", flag.ExitOnError)
	uiAddr := fset.String("ui", "127.0.0.1:7072", "where to serve the viewer")
	speed := fset.Float64("speed", 2, "playback speed (2 = twice as fast)")
	fset.Parse(args)
	if fset.NArg() != 1 || *speed <= 0 {
		fmt.Fprintln(os.Stderr, "usage: drill replay [--ui addr] [--speed 2] run.jsonl")
		return 2
	}
	f, err := os.Open(fset.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var events [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		if line := append([]byte(nil), sc.Bytes()...); len(line) > 0 {
			events = append(events, line)
		}
	}
	f.Close()
	if err := sc.Err(); err != nil || len(events) == 0 {
		fmt.Fprintln(os.Stderr, "replay: no events in", fset.Arg(0), err)
		return 1
	}
	s := &sink{}
	if err := s.serve(*uiAddr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "replaying %d events at %gx: %s\n", len(events), *speed, s.url)
	fmt.Fprintln(os.Stderr, "open the page now; playback starts in 3 seconds")
	time.Sleep(3 * time.Second)
	var last int64
	for _, ev := range events {
		var at struct {
			At int64 `json:"at"`
		}
		_ = json.Unmarshal(ev, &at)
		if last > 0 && at.At > last {
			gap := time.Duration(float64(at.At-last)/(*speed)) * time.Millisecond
			time.Sleep(min(gap, 4*time.Second))
		}
		if at.At > 0 {
			last = at.At
		}
		s.hub.Publish(ev)
	}
	s.close()
	return 0
}
