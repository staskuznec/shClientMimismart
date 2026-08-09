package shclient

import (
	"context"
	"log/slog"
	"time"
)

// Значения по умолчанию.
const (
	DefaultDialTimeout  = 10 * time.Second
	DefaultReadTimeout  = 15 * time.Second
	DefaultWriteTimeout = 10 * time.Second

	// DefaultPacketDelay — пауза между пакетами в батче. Сервер MimiSmart
	// хуже переносит отправку вплотную, поэтому пауза включена по умолчанию.
	DefaultPacketDelay = 10 * time.Millisecond

	// DefaultSettleDelay и DefaultDrainTimeout — время после отправки батча,
	// в течение которого клиент вычитывает ответы сервера. Без этого сервер
	// может закрыть соединение, решив, что клиент не слушает.
	DefaultSettleDelay  = 200 * time.Millisecond
	DefaultDrainTimeout = 500 * time.Millisecond
)

// options — внутренняя конфигурация клиента.
type options struct {
	dialTimeout  time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
	packetDelay  time.Duration
	settleDelay  time.Duration
	drainTimeout time.Duration
	autoDrain    bool
	udpRetrans   bool
	macID        string
	logger       *slog.Logger
}

func defaultOptions() options {
	return options{
		dialTimeout:  DefaultDialTimeout,
		readTimeout:  DefaultReadTimeout,
		writeTimeout: DefaultWriteTimeout,
		packetDelay:  DefaultPacketDelay,
		settleDelay:  DefaultSettleDelay,
		drainTimeout: DefaultDrainTimeout,
		autoDrain:    true,
		udpRetrans:   true,
		logger:       slog.New(discardHandler{}),
	}
}

// Option настраивает клиент в [New].
type Option func(*options)

// WithDialTimeout задаёт таймаут установки TCP-соединения.
func WithDialTimeout(d time.Duration) Option {
	return func(o *options) { o.dialTimeout = d }
}

// WithReadTimeout задаёт таймаут одной операции чтения.
func WithReadTimeout(d time.Duration) Option {
	return func(o *options) { o.readTimeout = d }
}

// WithWriteTimeout задаёт таймаут одной операции записи.
func WithWriteTimeout(d time.Duration) Option {
	return func(o *options) { o.writeTimeout = d }
}

// WithPacketDelay задаёт паузу между пакетами внутри батча.
// Ноль отключает паузу.
func WithPacketDelay(d time.Duration) Option {
	return func(o *options) { o.packetDelay = d }
}

// WithAutoDrain включает или выключает автоматическое вычитывание ответов
// сервера после [Client.Send]. По умолчанию включено; выключайте, если
// управляете этим вручную через [Client.Drain].
func WithAutoDrain(v bool) Option {
	return func(o *options) { o.autoDrain = v }
}

// WithDrainTimings настраивает паузу перед вычитыванием ответов и его
// длительность.
func WithDrainTimings(settle, drain time.Duration) Option {
	return func(o *options) {
		o.settleDelay = settle
		o.drainTimeout = drain
	}
}

// WithUDPRetranslate управляет атрибутом retranslate-udp в XML-запросе.
// По умолчанию включено.
func WithUDPRetranslate(v bool) Option {
	return func(o *options) { o.udpRetrans = v }
}

// WithMacID задаёт mac-id клиента. Если не задан, генерируется случайный
// на crypto/rand.
func WithMacID(mac string) Option {
	return func(o *options) { o.macID = mac }
}

// WithLogger включает журналирование. По умолчанию логи никуда не пишутся —
// библиотека не должна выбирать логгер за приложение.
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.logger = l
		}
	}
}

// discardHandler — slog.Handler, который ничего не делает.
// В Go 1.24 появился slog.DiscardHandler, но пакет собирается и на более
// старых версиях, поэтому обходимся своим.
type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (h discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return h }
func (h discardHandler) WithGroup(string) slog.Handler           { return h }
