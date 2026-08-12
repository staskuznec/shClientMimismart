package shclient

import (
	"context"
	"crypto/aes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// handshakeRetryDelay — пауза перед разбором следующего пакета, если сервер
// прислал что-то, кроме shcxml.
const handshakeRetryDelay = 100 * time.Millisecond

// Sender — узкий интерфейс отправки. Позволяет подменять клиент заглушкой
// в тестах приложения.
type Sender interface {
	Send(ctx context.Context, values ...Value) error
}

// Client — соединение с сервером умного дома. Безопасен для одновременного
// использования из нескольких горутин.
//
// Блокировок две, потому что отправка и приём должны идти одновременно.
// mu защищает состояние соединения и берётся только на время снимка — держать
// её весь батч нельзя, иначе на ней встанет цикл чтения. writeMu отвечает
// за сокет на запись и держится всю отправку, чтобы батч не перемешался
// с чужими пакетами. Чтение принадлежит одной горутине: либо [Client.Listen],
// либо [Client.Drain], одновременно им нельзя.
type Client struct {
	addr  string
	key   []byte
	macID string
	opts  options

	mu        sync.Mutex
	conn      net.Conn
	clientID  uint16
	listening bool

	writeMu sync.Mutex
}

// Проверка на этапе компиляции, что клиент реализует Sender.
var _ Sender = (*Client)(nil)

// New создаёт клиент и проверяет параметры до любых сетевых операций.
// Ключ должен подходить для AES: 16, 24 или 32 байта.
func New(addr, key string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(addr) == "" {
		return nil, ErrEmptyAddr
	}
	switch len(key) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("%w (получено %d)", ErrInvalidKey, len(key))
	}

	o := defaultOptions()
	for _, opt := range opts {
		opt(&o)
	}

	macID := o.macID
	if macID == "" {
		var err error
		if macID, err = randomMacID(); err != nil {
			return nil, err
		}
	}

	return &Client{addr: addr, key: []byte(key), macID: macID, opts: o}, nil
}

// MacID возвращает mac-id, которым клиент представляется серверу.
func (c *Client) MacID() string { return c.macID }

// ClientID возвращает идентификатор, полученный при рукопожатии.
// До успешного [Client.Connect] равен нулю.
func (c *Client) ClientID() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clientID
}

// Connected сообщает, установлено ли соединение.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Listening сообщает, запущен ли цикл чтения [Client.Listen].
func (c *Client) Listening() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listening
}

// state отдаёт снимок состояния соединения. Блокировка берётся только на
// время снимка: держать её всю операцию нельзя, иначе отправка и чтение
// начнут ждать друг друга.
func (c *Client) state() (conn net.Conn, clientID uint16, listening bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, 0, false, ErrNotConnected
	}
	return c.conn, c.clientID, c.listening, nil
}

// Connect устанавливает соединение, проходит авторизацию и рукопожатие.
// Отмена ctx прерывает операцию, в том числе уже начатый сетевой обмен.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return ErrAlreadyConnected
	}

	dialer := &net.Dialer{Timeout: c.opts.dialTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("shclient: подключение к %s: %w", c.addr, err)
	}

	// Отмена контекста разблокирует любое зависшее чтение или запись.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if err := c.authorize(conn); err != nil {
		_ = conn.Close()
		return err
	}
	clientID, err := c.handshake(conn)
	if err != nil {
		_ = conn.Close()
		return err
	}

	c.conn = conn
	c.clientID = clientID
	c.opts.logger.InfoContext(ctx, "shclient: соединение установлено",
		"addr", c.addr, "mac_id", c.macID, "client_id", clientID)
	return nil
}

// Close закрывает соединение. Повторный вызов безопасен.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// Send отправляет значения одним батчем в порядке перечисления.
//
// Между пакетами выдерживается пауза (см. [WithPacketDelay]), а после
// отправки клиент вычитывает ответы сервера (см. [WithAutoDrain]) — без этого
// сервер может закрыть соединение, решив, что клиент его не слушает. При
// запущенном [Client.Listen] вычитывание не нужно и не выполняется: ответы
// разбирает цикл чтения.
func (c *Client) Send(ctx context.Context, values ...Value) error {
	if len(values) == 0 {
		return nil
	}

	conn, clientID, listening, err := c.state()
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Срок ставится только на запись: общий SetDeadline сбил бы и цикл чтения.
	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer stop()

	for i, v := range values {
		if i > 0 && c.opts.packetDelay > 0 {
			if err := sleepCtx(ctx, c.opts.packetDelay); err != nil {
				return err
			}
		}
		if err := c.write(conn, v.pack(clientID)); err != nil {
			return fmt.Errorf("shclient: отправка значения %d из %d: %w", i+1, len(values), err)
		}
	}
	c.opts.logger.DebugContext(ctx, "shclient: батч отправлен", "count", len(values))

	if c.opts.autoDrain && !listening {
		if err := sleepCtx(ctx, c.opts.settleDelay); err != nil {
			return err
		}
		c.drain(conn, c.opts.drainTimeout)
	}
	return nil
}

// RequestAll просит сервер отдать состояния всех элементов. Ответ приходит
// пакетами [PDPingModule], которые разбирает [Client.Listen].
//
// Пакет уходит с идентификатором клиента, полученным при рукопожатии, и с
// нулевыми id и subid элемента: нули здесь означают «все устройства» и к
// client id отношения не имеют. Эталонные реализации на PHP и Python делают
// ровно так же.
//
// Этим же пакетом эталонный Python-клиент поддерживает соединение живым,
// повторяя его каждые пять секунд, — см. [WithKeepalive].
func (c *Client) RequestAll(ctx context.Context) error {
	conn, clientID, _, err := c.state()
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	stop := context.AfterFunc(ctx, func() { _ = conn.SetWriteDeadline(time.Now()) })
	defer stop()

	if err := c.write(conn, packPacket(clientID, 0, 0, PDRequestAllDevices, nil)); err != nil {
		return fmt.Errorf("shclient: запрос состояний: %w", err)
	}
	c.opts.logger.DebugContext(ctx, "shclient: запрошены состояния всех элементов")
	return nil
}

// Drain вычитывает и отбрасывает всё, что сервер прислал в течение d.
// Нужен, если автоматическое вычитывание отключено через [WithAutoDrain].
//
// Пока работает [Client.Listen], вычитывать нечего и нельзя: вернётся
// [ErrAlreadyListening].
func (c *Client) Drain(ctx context.Context, d time.Duration) error {
	conn, _, listening, err := c.state()
	if err != nil {
		return err
	}
	if listening {
		return ErrAlreadyListening
	}

	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()

	c.drain(conn, d)
	return nil
}

// Handler обрабатывает событие от сервера.
//
// Вызывается синхронно из цикла чтения: пока Handler работает, следующие
// пакеты не читаются. Поэтому события не теряются и не переупорядочиваются,
// но затянутая обработка тормозит приём — тяжёлую работу уносите в свою
// очередь.
type Handler func(Event)

// Listen читает пакеты от сервера и отдаёт их обработчику, пока не будет
// отменён контекст или не порвётся соединение.
//
// Возвращает [ErrAlreadyListening], если цикл уже запущен: чтение из сокета
// принадлежит одной горутине. Отправлять значения во время работы Listen
// можно — запись и чтение разведены.
//
// Отмена контекста завершает цикл и возвращает ошибку контекста. Обрыв
// соединения возвращается как ошибка чтения; переподключение — дело
// вызывающего кода, клиент готов к повторному [Client.Connect] после
// [Client.Close].
func (c *Client) Listen(ctx context.Context, h Handler) error {
	if h == nil {
		return ErrNilHandler
	}

	conn, err := c.startListen()
	if err != nil {
		return err
	}
	defer c.stopListen()

	// cancelled нужен, потому что срок чтения переставляется на каждой
	// итерации и может затереть тот, что выставила отмена контекста.
	var cancelled atomic.Bool
	stop := context.AfterFunc(ctx, func() {
		cancelled.Store(true)
		_ = conn.SetReadDeadline(time.Now())
	})
	defer stop()

	if c.opts.keepalive > 0 {
		kctx, cancel := context.WithCancel(ctx)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.keepaliveLoop(kctx)
		}()
		// Порядок важен: сначала отменяем контекст, потом ждём горутину.
		defer wg.Wait()
		defer cancel()
	}

	c.opts.logger.InfoContext(ctx, "shclient: цикл чтения запущен")
	return c.readLoop(ctx, conn, &cancelled, h)
}

// readLoop — сам цикл чтения. Вынесен отдельно, чтобы Listen остался про
// запуск и остановку, а не про разбор кадров.
func (c *Client) readLoop(ctx context.Context, conn net.Conn, cancelled *atomic.Bool, h Handler) error {
	head := make([]byte, headerSize)
	for {
		// Ожидание следующего пакета не ограничено readTimeout: между
		// событиями в тихом доме проходят часы. Ограничивает только
		// [WithIdleTimeout], если он задан.
		var wait time.Time
		if c.opts.idleTimeout > 0 {
			wait = time.Now().Add(c.opts.idleTimeout)
		}
		if err := setReadDeadline(conn, cancelled, wait); err != nil {
			return err
		}
		if _, err := io.ReadFull(conn, head); err != nil {
			return c.readError(ctx, cancelled, err, true)
		}

		in := parseInHeader(head)
		payload := make([]byte, in.length)
		if in.length > 0 {
			// А вот хвост пакета обязан прийти сразу: заголовок уже прочитан.
			if err := setReadDeadline(conn, cancelled, time.Now().Add(c.opts.readTimeout)); err != nil {
				return err
			}
			if _, err := io.ReadFull(conn, payload); err != nil {
				return c.readError(ctx, cancelled, err, false)
			}
		}
		at := time.Now()

		if in.pd == PDPingModule {
			// Ответ модуля — это состояния всех его элементов сразу.
			// Наружу отдаём по событию на элемент: потребителю всё равно,
			// пришло состояние поодиночке или пачкой.
			states, tail := splitModuleStates(payload)
			for _, st := range states {
				h(Event{
					SenderID:    in.senderID,
					SenderSubID: st.subID,
					PD:          in.pd,
					Payload:     st.payload,
					At:          at,
				})
			}
			if tail > 0 {
				c.opts.logger.WarnContext(ctx, "shclient: хвост ответа модуля не разобран",
					"sender_id", in.senderID, "bytes", tail)
			}
			continue
		}

		h(Event{
			SenderID:    in.senderID,
			SenderSubID: in.senderSubID,
			PD:          in.pd,
			Payload:     payload,
			At:          at,
		})
	}
}

// keepaliveLoop повторяет запрос состояний, пока жив контекст: эталонный
// Python-клиент так же не даёт серверу выкинуть себя по таймауту.
func (c *Client) keepaliveLoop(ctx context.Context) {
	t := time.NewTicker(c.opts.keepalive)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := c.RequestAll(ctx); err != nil {
				// Писать больше некуда: соединение порвано, и цикл чтения
				// вот-вот вернёт ту же ошибку вызывающему.
				c.opts.logger.DebugContext(ctx, "shclient: keepalive не отправлен", "err", err)
				return
			}
		}
	}
}

// startListen занимает чтение под цикл Listen.
func (c *Client) startListen() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, ErrNotConnected
	}
	if c.listening {
		return nil, ErrAlreadyListening
	}
	c.listening = true
	return c.conn, nil
}

func (c *Client) stopListen() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listening = false
}

// setReadDeadline ставит срок чтения, не затирая отмену контекста.
//
// Проверки до и после установки закрывают гонку с [context.AfterFunc]: если
// отмена случилась между ними, срок возвращается на «сейчас» нашими же руками,
// а если после — выигрывает установка из AfterFunc. Нулевое время t означает
// чтение без ограничения по сроку.
func setReadDeadline(conn net.Conn, cancelled *atomic.Bool, t time.Time) error {
	if cancelled.Load() {
		return nil // срок уже выставлен отменой, чтение вернётся с ошибкой
	}
	if err := conn.SetReadDeadline(t); err != nil {
		return err
	}
	if cancelled.Load() {
		_ = conn.SetReadDeadline(time.Now())
	}
	return nil
}

// readError переводит ошибку чтения в ту, которую стоит показать вызывающему.
// Флаг idle различает ожидание следующего пакета и чтение хвоста уже начатого.
func (c *Client) readError(ctx context.Context, cancelled *atomic.Bool, err error, idle bool) error {
	if cancelled.Load() {
		return ctx.Err()
	}
	var netErr net.Error
	if idle && c.opts.idleTimeout > 0 && errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: тишина дольше %s", ErrIdleTimeout, c.opts.idleTimeout)
	}
	return fmt.Errorf("shclient: чтение пакета: %w", err)
}

// authorize отвечает на challenge сервера: 16 байт шифруются одним блоком AES.
func (c *Client) authorize(conn net.Conn) error {
	challenge, err := c.read(conn, challengeSize)
	if err != nil {
		return fmt.Errorf("shclient: чтение challenge: %w", err)
	}

	block, err := aes.NewCipher(c.key)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}

	response := make([]byte, challengeSize)
	block.Encrypt(response, challenge)

	if err := c.write(conn, response); err != nil {
		return fmt.Errorf("shclient: отправка ответа на challenge: %w", err)
	}
	return nil
}

// handshake запрашивает логику (get-shc) и вычисляет идентификатор клиента.
func (c *Client) handshake(conn net.Conn) (uint16, error) {
	xml := c.buildXMLRequest()
	frame := make([]byte, 4, 4+len(xml))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(xml)))
	frame = append(frame, xml...)

	if err := c.write(conn, frame); err != nil {
		return 0, fmt.Errorf("shclient: отправка XML-запроса: %w", err)
	}

	for attempt := 0; attempt < handshakeAttempts; attempt++ {
		head, err := c.read(conn, replyHeadSize)
		if err != nil {
			return 0, fmt.Errorf("shclient: чтение заголовка ответа: %w", err)
		}
		length := int(binary.LittleEndian.Uint32(head[0:4]))
		tag := string(head[4:replyHeadSize])

		switch tag {
		case tagSHCXML:
			crc, err := c.read(conn, crcByteSize)
			if err != nil {
				return 0, fmt.Errorf("shclient: чтение CRC: %w", err)
			}
			clientID := initClientDefValue - uint16(crc[crcByteSize-1])

			// Остаток пакета — сама логика. Вычитать её надо в любом случае,
			// иначе разъедутся границы кадров; отдаём ли мы её наружу,
			// решает [WithLogicSink].
			if rest := length - replyTagSize - crcByteSize; rest > 0 {
				if err := c.readLogic(conn, int64(rest)); err != nil {
					return 0, err
				}
			}
			return clientID, nil

		case tagPKFail:
			return 0, ErrInvalidServerKey

		default:
			if rest := length - replyTagSize; rest > 0 {
				if err := c.discard(conn, int64(rest)); err != nil {
					return 0, fmt.Errorf("shclient: пропуск пакета %q: %w", tag, err)
				}
			}
			time.Sleep(handshakeRetryDelay)
		}
	}
	return 0, ErrHandshakeFailed
}

// buildXMLRequest собирает запрос get-shc.
func (c *Client) buildXMLRequest() []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<smart-house-commands>\n")
	if c.opts.udpRetrans {
		fmt.Fprintf(&b, "<get-shc retranslate-udp=\"yes\" mac-id=\"%s\"/>\n", c.macID)
	} else {
		fmt.Fprintf(&b, "<get-shc mac-id=\"%s\"/>\n", c.macID)
	}
	b.WriteString("</smart-house-commands>\n")
	return []byte(b.String())
}

// read читает ровно size байт с таймаутом.
func (c *Client) read(conn net.Conn, size int) ([]byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(c.opts.readTimeout)); err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// write отправляет буфер целиком с таймаутом.
func (c *Client) write(conn net.Conn, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(c.opts.writeTimeout)); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

// readLogic вычитывает n байт logic.xml: отдаёт их приёмнику, если он задан
// через [WithLogicSink], иначе просто отбрасывает.
func (c *Client) readLogic(conn net.Conn, n int64) error {
	if c.opts.logicSink == nil {
		if err := c.discard(conn, n); err != nil {
			return fmt.Errorf("shclient: пропуск XML-логики: %w", err)
		}
		return nil
	}

	if err := conn.SetReadDeadline(time.Now().Add(c.opts.readTimeout)); err != nil {
		return err
	}
	sink := &logicSink{w: c.opts.logicSink}
	if _, err := io.CopyN(sink, conn, n); err != nil {
		return fmt.Errorf("shclient: чтение XML-логики: %w", err)
	}
	if sink.err != nil {
		return fmt.Errorf("shclient: запись XML-логики: %w", sink.err)
	}
	return nil
}

// logicSink пишет в приёмник пользователя и запоминает первую его ошибку,
// но сам ошибку наружу не отдаёт: поток из сокета нужно дочитать до конца
// в любом случае, иначе хвост логики останется в сокете и следующий кадр
// разъедется.
type logicSink struct {
	w   io.Writer
	err error
}

func (s *logicSink) Write(p []byte) (int, error) {
	if s.err == nil {
		if _, err := s.w.Write(p); err != nil {
			s.err = err
		}
	}
	return len(p), nil
}

// discard вычитывает и отбрасывает n байт без лишних аллокаций.
func (c *Client) discard(conn net.Conn, n int64) error {
	if err := conn.SetReadDeadline(time.Now().Add(c.opts.readTimeout)); err != nil {
		return err
	}
	_, err := io.CopyN(io.Discard, conn, n)
	return err
}

// drain читает всё доступное в течение d и отбрасывает.
func (c *Client) drain(conn net.Conn, d time.Duration) {
	if d <= 0 {
		return
	}
	if err := conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		return
	}
	n, err := io.Copy(io.Discard, conn)
	if n > 0 {
		c.opts.logger.Debug("shclient: ответы сервера вычитаны", "bytes", n)
	}
	// Таймаут здесь — штатное завершение, а не ошибка.
	var netErr net.Error
	if err != nil && !(errors.As(err, &netErr) && netErr.Timeout()) {
		c.opts.logger.Debug("shclient: чтение ответов прервано", "err", err)
	}
	_ = conn.SetReadDeadline(time.Time{})
}

// sleepCtx ждёт указанное время или отмену контекста.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// randomMacID генерирует случайный mac-id на crypto/rand.
func randomMacID() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("shclient: генерация mac-id: %w", err)
	}
	return fmt.Sprintf("%02x%02x%02x%02x%02x%02x", b[0], b[1], b[2], b[3], b[4], b[5]), nil
}
