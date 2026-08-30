package api

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// devWebhookRecord is a single received payload for the dev echo endpoint.
type devWebhookRecord struct {
	ID        string `json:"id"`
	Received  string `json:"received"`
	Headers   map[string]string `json:"headers"`
	Body      string `json:"body"`
}

var (
	devWebhooksMu sync.Mutex
	devWebhooks   []devWebhookRecord
	devWebhookID  int
)

// DevWebhookHandler receives POSTs and stores the payload in memory for
// manual inspection; GET returns the list of received payloads.
// Only registered in development mode.
func DevWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		devWebhooksMu.Lock()
		out := make([]devWebhookRecord, len(devWebhooks))
		copy(out, devWebhooks)
		devWebhooksMu.Unlock()
		JSON(w, r, http.StatusOK, map[string]any{"records": out})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, _ := io.ReadAll(r.Body)
	devWebhookID++
	rec := devWebhookRecord{
		ID:       fmt.Sprintf("%d", devWebhookID),
		Received: time.Now().UTC().Format(time.RFC3339),
		Headers:  map[string]string{},
		Body:     string(body),
	}
	for k, vs := range r.Header {
		if len(vs) > 0 {
			rec.Headers[k] = vs[0]
		}
	}

	devWebhooksMu.Lock()
	devWebhooks = append(devWebhooks, rec)
	if len(devWebhooks) > 100 {
		devWebhooks = devWebhooks[1:]
	}
	devWebhooksMu.Unlock()

	JSON(w, r, http.StatusOK, map[string]any{"recorded": rec.ID})
}