package ws

import (
	"net/http"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Dev/test: allow all origins. Production should restrict.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler upgrades HTTP to WebSocket market-data streams.
type Handler struct {
	hub *marketdata.Hub
}

// NewHandler wraps a marketdata Hub.
func NewHandler(hub *marketdata.Hub) *Handler {
	return &Handler{hub: hub}
}

// Register mounts GET /v1/market-data/ws
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/market-data/ws", h.serveWS)
}

func (h *Handler) serveWS(w http.ResponseWriter, r *http.Request) {
	if !h.hub.Ready() {
		http.Error(w, `{"type":"error","code":"NOT_READY","message":"market data not ready"}`, http.StatusServiceUnavailable)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newClient(h.hub, conn)
	h.hub.AddSubscriber(c)
	go c.writePump()
	go c.readPump()
}
