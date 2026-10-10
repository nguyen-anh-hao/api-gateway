package handler

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSMarketData implements a minimal RFC6455 WebSocket server that pushes
// market-data streams (orderbook@100ms, trades) for a given symbol.
func (h *Handler) WSMarketData(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if key == "" {
		http.Error(w, "missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}
	symbol := r.URL.Query().Get("symbol")

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket unsupported", http.StatusInternalServerError)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	sum := sha1.Sum([]byte(key + wsGUID))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, _ = conn.Write([]byte(
		"HTTP/1.1 101 Switching Protocols\r\n" +
			"Upgrade: websocket\r\n" +
			"Connection: Upgrade\r\n" +
			"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"))

	subs := map[string]bool{}
	readErr := make(chan struct{})
	go func() {
		defer close(readErr)
		for {
			opcode, payload, err := readFrame(buf.Reader)
			if err != nil {
				return
			}
			if opcode == 0x8 { // close
				return
			}
			if opcode == 0x1 { // text
				msg := strings.TrimSpace(string(payload))
				if strings.HasPrefix(msg, "SUBSCRIBE ") {
					subs[strings.TrimSpace(strings.TrimPrefix(msg, "SUBSCRIBE "))] = true
				}
			}
		}
	}()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-readErr:
			return
		case <-ticker.C:
			if subs["orderbook@100ms"] && symbol != "" {
				if book, err := h.Engine.OrderBook(context.Background(), symbol, 10); err == nil {
					payload, _ := json.Marshal(map[string]interface{}{
						"stream": "orderbook@100ms", "data": book,
					})
					if err := writeTextFrame(conn, string(payload)); err != nil {
						return
					}
				}
			}
			if subs["trades"] {
				trades := h.recentTrades(symbol)
				payload, _ := json.Marshal(map[string]interface{}{
					"stream": "trades", "data": trades,
				})
				if err := writeTextFrame(conn, string(payload)); err != nil {
					return
				}
			}
		}
	}
}

func (h *Handler) recentTrades(symbol string) []TradeView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []TradeView{}
	for i := len(h.tape) - 1; i >= 0 && len(out) < 20; i-- {
		t := h.tape[i]
		if symbol == "" || t.Symbol == symbol {
			out = append(out, t)
		}
	}
	return out
}

// readFrame reads a single (client, masked) WebSocket frame.
func readFrame(r *bufio.Reader) (byte, []byte, error) {
	b0, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	b1, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode := b0 & 0x0f
	masked := b1&0x80 != 0
	length := uint64(b1 & 0x7f)
	switch length {
	case 126:
		var ext uint16
		if err := binary.Read(r, binary.BigEndian, &ext); err != nil {
			return 0, nil, err
		}
		length = uint64(ext)
	case 127:
		var ext uint64
		if err := binary.Read(r, binary.BigEndian, &ext); err != nil {
			return 0, nil, err
		}
		length = ext
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

// writeTextFrame writes a single (server, unmasked) text frame.
func writeTextFrame(conn net.Conn, text string) error {
	payload := []byte(text)
	var hdr []byte
	hdr = append(hdr, 0x81) // FIN + text opcode
	switch {
	case len(payload) < 126:
		hdr = append(hdr, byte(len(payload)))
	case len(payload) < 65536:
		hdr = append(hdr, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		hdr = append(hdr, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(len(payload)))
		hdr = append(hdr, ext[:]...)
	}
	hdr = append(hdr, payload...)
	_, err := conn.Write(hdr)
	return err
}
