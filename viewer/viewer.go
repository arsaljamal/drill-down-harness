// Package viewer shows an investigation in the browser as it happens.
//
// Events are JSON objects with a "kind" (meta, model_reply, tool_call,
// tool_result, lens, gate, retry, stop, summary, final, and host additions
// such as context and verdict). A Hub keeps every event so a page opened
// late, or a replay, sees the whole story.
package viewer

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
)

//go:embed ui
var ui embed.FS

// Page serves the viewer page. Hosts that stream events themselves (for
// example a service's control room) can mount it and point it at their own
// stream with ?src=<url>.
func Page() http.Handler {
	sub, _ := fs.Sub(ui, "ui")
	return http.FileServerFS(sub)
}

// Hub fans events out to every open page and remembers them for latecomers.
type Hub struct {
	mu      sync.Mutex
	history [][]byte
	subs    map[chan []byte]struct{}
	closed  bool
}

func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

// Publish sends one event (a JSON object) to all pages.
func (h *Hub) Publish(event []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history = append(h.history, event)
	for c := range h.subs {
		select {
		case c <- event:
		default: // a stuck page must not stall the investigation
		}
	}
}

// Close tells pages the stream is complete.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for c := range h.subs {
		close(c)
		delete(h.subs, c)
	}
}

// Handler serves the page at / and the event stream at /events.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", Page())
	mux.HandleFunc("GET /events", h.stream)
	return mux
}

func (h *Hub) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	h.mu.Lock()
	past := append([][]byte(nil), h.history...)
	var live chan []byte
	if !h.closed {
		live = make(chan []byte, 256)
		h.subs[live] = struct{}{}
	}
	h.mu.Unlock()
	defer func() {
		if live != nil {
			h.mu.Lock()
			if _, ok := h.subs[live]; ok {
				delete(h.subs, live)
			}
			h.mu.Unlock()
		}
	}()

	send := func(ev []byte) {
		fmt.Fprintf(w, "event: step\ndata: %s\n\n", ev)
	}
	for _, ev := range past {
		send(ev)
	}
	flusher.Flush()
	if live == nil {
		fmt.Fprint(w, "event: end\ndata: {}\n\n")
		flusher.Flush()
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-live:
			if !ok {
				fmt.Fprint(w, "event: end\ndata: {}\n\n")
				flusher.Flush()
				return
			}
			send(ev)
			flusher.Flush()
		}
	}
}
