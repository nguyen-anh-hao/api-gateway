// Package eventbus publishes domain events from the API Gateway to the NATS
// message bus. The gateway is only a thin producer: it forwards each executed
// trade to market-data-service (and any future subscribers) without storing or
// aggregating market data itself.
package eventbus

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	// StreamName is the JetStream stream holding trade events.
	StreamName = "TRADES"
	// TradeSubject is the subject market-data-service subscribes to.
	TradeSubject = "market.trades.executed"
)

// TradeEvent is the wire contract for an executed trade. It is duplicated (by
// convention) in market-data-service/internal/domain. Field names are the JSON
// contract shared between the two services.
type TradeEvent struct {
	TradeID        uint64 `json:"trade_id"`
	Symbol         string `json:"symbol"`
	Price          string `json:"price"`
	Quantity       string `json:"quantity"`
	Fee            string `json:"fee"`
	Role           string `json:"role"`
	Timestamp      int64  `json:"timestamp_ns"`
	MakerOrderID   uint64 `json:"maker_order_id"`
	TakerOrderID   uint64 `json:"taker_order_id"`
	MakerAccountID uint64 `json:"maker_account_id"`
	TakerAccountID uint64 `json:"taker_account_id"`
}

// Publisher buffers events and drains them to NATS on a background goroutine so
// the order hot path never blocks on network I/O. Publish is best-effort: when
// NATS is unreachable or the buffer is full, events are dropped.
type Publisher struct {
	ch chan []byte
}

// NewPublisher starts a background publisher goroutine. A blank addr disables
// publishing (the returned Publisher simply drops everything).
func NewPublisher(addr string) *Publisher {
	p := &Publisher{ch: make(chan []byte, 4096)}
	go p.run(addr)
	return p
}

// Publish enqueues an event without blocking the caller.
func (p *Publisher) Publish(t TradeEvent) {
	b, err := json.Marshal(t)
	if err != nil {
		slog.Error("eventbus: marshal failed", "err", err)
		return
	}
	select {
	case p.ch <- b:
	default:
		slog.Warn("eventbus: buffer full, dropping trade event")
	}
}

func (p *Publisher) run(addr string) {
	if addr == "" {
		slog.Warn("eventbus: NATS_ADDR unset, trade events disabled")
		return
	}
	for {
		if err := p.drain(addr); err != nil {
			slog.Error("eventbus: publisher error", "err", err)
		}
		time.Sleep(2 * time.Second)
	}
}

func (p *Publisher) drain(addr string) error {
	nc, err := nats.Connect(addr,
		nats.Timeout(2*time.Second),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(500*time.Millisecond),
	)
	if err != nil {
		return err
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:     StreamName,
		Subjects: []string{TradeSubject},
	}); err != nil {
		return err
	}

	for b := range p.ch {
		if _, err := js.Publish(TradeSubject, b); err != nil {
			slog.Error("eventbus: publish failed", "err", err)
		}
	}
	return nil
}
