package stream

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
)

// Event is a typed message published to SSE subscribers.
type Event struct {
	Type    string `json:"type"`    // "thought", "tool_call", "tool_result", "status", "plan"
	TaskID  string `json:"task_id"`
	Payload any    `json:"payload"`
}

// subscriber holds a channel for a single SSE client.
type subscriber struct {
	ch chan Event
}

// Broker manages SSE subscriptions and event fan-out per task.
type Broker struct {
	mu   sync.RWMutex
	subs map[string][]*subscriber // taskID -> list of subscribers
}

// NewBroker creates a ready-to-use Broker.
func NewBroker() *Broker {
	return &Broker{
		subs: make(map[string][]*subscriber),
	}
}

// Subscribe registers a new SSE listener for the given taskID.
// The returned channel receives events published to that task.
// Call the returned unsubscribe function when the client disconnects.
func (b *Broker) Subscribe(taskID string) (<-chan Event, func()) {
	sub := &subscriber{ch: make(chan Event, 64)}

	b.mu.Lock()
	b.subs[taskID] = append(b.subs[taskID], sub)
	b.mu.Unlock()

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()

		list := b.subs[taskID]
		for i, s := range list {
			if s == sub {
				b.subs[taskID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		// Close the channel after removal so ServeSSE can exit cleanly
		close(sub.ch)
	}

	return sub.ch, unsubscribe
}

// Publish broadcasts an event to every subscriber of the event's task.
// Slow consumers are skipped (non-blocking send) to avoid stalling the agent.
func (b *Broker) Publish(event Event) {
	b.mu.RLock()
	list := b.subs[event.TaskID]
	// Copy the slice under the read lock so we can release it before sending
	targets := make([]*subscriber, len(list))
	copy(targets, list)
	b.mu.RUnlock()

	for _, sub := range targets {
		select {
		case sub.ch <- event:
		default:
			slog.Warn("SSE subscriber channel full, dropping event",
				"task_id", event.TaskID,
				"event_type", event.Type,
			)
		}
	}
}

// ServeSSE handles an HTTP request as a Server-Sent Events stream for the given task.
// It blocks until the client disconnects or the request context is cancelled.
func (b *Broker) ServeSSE(w http.ResponseWriter, r *http.Request, taskID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	ch, unsubscribe := b.Subscribe(taskID)
	defer unsubscribe()

	// Send a connected ping so the client knows the stream is live
	fmt.Fprintf(w, "event: connected\ndata: {\"task_id\":%q}\n\n", taskID)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return

		case event, open := <-ch:
			if !open {
				return
			}

			data, err := json.Marshal(event)
			if err != nil {
				slog.Error("failed to marshal SSE event", "error", err)
				continue
			}

			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		}
	}
}

