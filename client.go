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
type Client struct {
	addr  string
	key   []byte
	macID string
	opts  options

	mu       sync.Mutex
	conn     net.Conn
	clientID uint16
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
// сервер может закрыть соединение, решив, что клиент его не слушает.
func (c *Client) Send(ctx context.Context, values ...Value) error {
	if len(values) == 0 {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}
	conn := c.conn

	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	for i, v := range values {
		if i > 0 && c.opts.packetDelay > 0 {
			if err := sleepCtx(ctx, c.opts.packetDelay); err != nil {
				return err
			}
		}
		if err := c.write(conn, v.pack(c.clientID)); err != nil {
			return fmt.Errorf("shclient: отправка значения %d из %d: %w", i+1, len(values), err)
		}
	}
	c.opts.logger.DebugContext(ctx, "shclient: батч отправлен", "count", len(values))

	if c.opts.autoDrain {
		if err := sleepCtx(ctx, c.opts.settleDelay); err != nil {
			return err
		}
		c.drain(conn, c.opts.drainTimeout)
	}
	return nil
}

// RequestAll просит сервер отдать состояния всех элементов.
//
// Ответ приходит пачками пакетов [PDPingModule]; эта версия клиента их не
// разбирает, поэтому вычитать их нужно самостоятельно — через [Client.Drain]
// или автоматическим вычитыванием после [Client.Send].
//
// Пакет уходит с нулевым идентификатором клиента, а не с полученным при
// рукопожатии: так делают эталонные реализации.
func (c *Client) RequestAll(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}
	conn := c.conn

	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	if err := c.write(conn, packPacket(0, 0, 0, PDRequestAllDevices, nil)); err != nil {
		return fmt.Errorf("shclient: запрос состояний: %w", err)
	}
	c.opts.logger.DebugContext(ctx, "shclient: запрошены состояния всех элементов")
	return nil
}

// Drain вычитывает и отбрасывает всё, что сервер прислал в течение d.
// Нужен, если автоматическое вычитывание отключено через [WithAutoDrain].
func (c *Client) Drain(ctx context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return ErrNotConnected
	}
	conn := c.conn

	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	c.drain(conn, d)
	return nil
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
