package shclient

import (
	"context"
	"io"
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

	// ReferenceKeepalive — период, с которым эталонный Python-клиент шлёт
	// запрос состояний, чтобы сервер не выкинул его по таймауту.
	// Значение по умолчанию не задаётся: см. [WithKeepalive].
	ReferenceKeepalive = 5 * time.Second
)

// options — внутренняя конфигурация клиента.
type options struct {
	dialTimeout  time.Duration
	readTimeout  time.Duration
	writeTimeout time.Duration
	packetDelay  time.Duration
	settleDelay  time.Duration
	drainTimeout time.Duration
	keepalive    time.Duration
	idleTimeout  time.Duration
	autoDrain    bool
	udpRetrans   bool
	macID        string
	logger       *slog.Logger
	logicSink    io.Writer
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

// WithKeepalive включает поддержание соединения: пока работает
// [Client.Listen], клиент раз в d повторяет [Client.RequestAll], чтобы сервер
// не выкинул его по таймауту. Ноль (по умолчанию) отключает поддержание.
//
// Эталонный Python-клиент делает это каждые [ReferenceKeepalive].
func WithKeepalive(d time.Duration) Option {
	return func(o *options) { o.keepalive = d }
}

// WithIdleTimeout задаёт, сколько [Client.Listen] готов ждать следующего
// пакета. По истечении цикл завершается с [ErrIdleTimeout]. Ноль (по
// умолчанию) означает ждать сколько угодно.
//
// Это единственный быстрый способ заметить, что связь оборвалась молча:
// оборванное TCP-соединение само себя не обнаруживает, а запись в него
// продолжает удаваться, пока не переполнится буфер. Имеет смысл только
// вместе с [WithKeepalive] — иначе тишина в спокойном доме нормальна, — и
// значение стоит брать кратным периоду поддержания, например втрое больше.
func WithIdleTimeout(d time.Duration) Option {
	return func(o *options) { o.idleTimeout = d }
}

// WithLogicSink задаёт приёмник для logic.xml — описания областей и элементов,
// которое сервер присылает в ответе на рукопожатие: адреса, имена и типы.
// По умолчанию логика отбрасывается, потому что держать её в памяти нужно
// далеко не всем.
//
// Байты пишутся как есть, без разбора XML: что с ними делать, решает
// вызывающий код. Запись идёт внутри [Client.Connect], потоком, поэтому
// приёмником может быть файл — весь документ в памяти собирать не обязательно.
//
// Если приёмник вернёт ошибку, [Client.Connect] завершится ошибкой: молча
// потерять данные, которые у нас попросили, хуже, чем не подключиться.
func WithLogicSink(w io.Writer) Option {
	return func(o *options) { o.logicSink = w }
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
