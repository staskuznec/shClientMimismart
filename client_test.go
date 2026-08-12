package shclient

import (
	"bytes"
	"context"
	"crypto/aes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

const testKey = "0123456789abcdef" // ровно 16 байт

// fakeServer — минимальный сервер SHS для тестов.
type fakeServer struct {
	t        *testing.T
	ln       net.Listener
	serverB  byte   // байт, из которого клиент вычисляет client id
	reply    string // тег ответа на XML-запрос
	logic    []byte // тело logic.xml в ответе shcxml
	junkTags []string

	mu       sync.Mutex
	received []byte // всё, что пришло после рукопожатия
	xmlSent  string // XML-запрос, полученный от клиента
	done     chan struct{}
}

func newFakeServer(t *testing.T, serverB byte, reply string, junkTags ...string) *fakeServer {
	t.Helper()
	return newFakeServerLogic(t, serverB, reply, []byte("<logic/>"), junkTags...)
}

func newFakeServerLogic(t *testing.T, serverB byte, reply string, logic []byte, junkTags ...string) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{
		t: t, ln: ln, serverB: serverB, reply: reply,
		logic: logic, junkTags: junkTags, done: make(chan struct{}),
	}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) addr() string { return s.ln.Addr().String() }

func (s *fakeServer) Received() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.received...)
}

func (s *fakeServer) XMLRequest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.xmlSent
}

func (s *fakeServer) serve() {
	defer close(s.done)

	conn, err := s.ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()

	// 1. Отправляем challenge и проверяем ответ клиента.
	challenge := bytes.Repeat([]byte{0x5A}, challengeSize)
	if _, err := conn.Write(challenge); err != nil {
		return
	}
	got := make([]byte, challengeSize)
	if _, err := io.ReadFull(conn, got); err != nil {
		return
	}
	block, err := aes.NewCipher([]byte(testKey))
	if err != nil {
		return
	}
	want := make([]byte, challengeSize)
	block.Encrypt(want, challenge)
	if !bytes.Equal(got, want) {
		s.t.Errorf("сервер получил неверный ответ на challenge")
		return
	}

	// 2. Читаем XML-запрос: 4 байта длины + тело.
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return
	}
	xmlBuf := make([]byte, binary.LittleEndian.Uint32(lenBuf))
	if _, err := io.ReadFull(conn, xmlBuf); err != nil {
		return
	}
	s.mu.Lock()
	s.xmlSent = string(xmlBuf)
	s.mu.Unlock()

	// 3. Сначала мусорные пакеты, которые клиент обязан пропустить.
	for _, tag := range s.junkTags {
		if _, err := conn.Write(junkFrame(tag, []byte("payload"))); err != nil {
			return
		}
	}

	// 4. Затем ответ на рукопожатие.
	switch s.reply {
	case tagPKFail:
		if _, err := conn.Write(junkFrame(tagPKFail, nil)); err != nil {
			return
		}
		return
	case tagSHCXML:
		if _, err := conn.Write(shcxmlFrame(s.serverB, s.logic)); err != nil {
			return
		}
	default:
		return
	}

	// 5. Копим всё, что клиент пришлёт дальше.
	buf := make([]byte, 4096)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, err := conn.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.received = append(s.received, buf[:n]...)
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// shcxmlFrame собирает успешный ответ рукопожатия.
// length считает тег, CRC с байтом сервера и полезную нагрузку.
func shcxmlFrame(serverByte byte, payload []byte) []byte {
	body := make([]byte, 0, crcByteSize+len(payload))
	body = append(body, 0xDE, 0xAD, 0xBE, 0xEF) // CRC, клиентом не проверяется
	body = append(body, serverByte)
	body = append(body, payload...)

	frame := make([]byte, 4)
	binary.LittleEndian.PutUint32(frame, uint32(replyTagSize+len(body)))
	frame = append(frame, []byte(tagSHCXML)...)
	return append(frame, body...)
}

// junkFrame собирает пакет с произвольным тегом.
func junkFrame(tag string, payload []byte) []byte {
	frame := make([]byte, 4)
	binary.LittleEndian.PutUint32(frame, uint32(replyTagSize+len(payload)))
	frame = append(frame, []byte(tag)...)
	return append(frame, payload...)
}

// testClient — клиент с отключёнными паузами, чтобы тесты шли быстро.
func testClient(t *testing.T, addr string, extra ...Option) *Client {
	t.Helper()
	opts := append([]Option{
		WithMacID("aabbccddeeff"),
		WithPacketDelay(0),
		WithAutoDrain(false),
		WithDialTimeout(2 * time.Second),
		WithReadTimeout(2 * time.Second),
		WithWriteTimeout(2 * time.Second),
	}, extra...)
	c, err := New(addr, testKey, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	if _, err := New("", testKey); !errors.Is(err, ErrEmptyAddr) {
		t.Errorf("пустой адрес: получено %v", err)
	}
	for _, key := range []string{"", "short", "0123456789abcde", "0123456789abcdefg"} {
		if _, err := New("127.0.0.1:1", key); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ключ длиной %d: получено %v", len(key), err)
		}
	}
	for _, key := range []string{"0123456789abcdef", "0123456789abcdef01234567", "0123456789abcdef0123456789abcdef"} {
		if _, err := New("127.0.0.1:1", key); err != nil {
			t.Errorf("ключ длиной %d отвергнут: %v", len(key), err)
		}
	}
}

func TestConnectComputesClientID(t *testing.T) {
	const serverByte = 0x10
	s := newFakeServer(t, serverByte, tagSHCXML)
	c := testClient(t, s.addr())

	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	want := initClientDefValue - uint16(serverByte)
	if got := c.ClientID(); got != want {
		t.Errorf("client id = %d, ожидался %d", got, want)
	}
	if !c.Connected() {
		t.Error("Connected() = false после успешного Connect")
	}
}

// TestXMLRequestMatchesReference сверяет XML-запрос с эталонной реализацией.
func TestXMLRequestMatchesReference(t *testing.T) {
	for _, udp := range []bool{true, false} {
		s := newFakeServer(t, 1, tagSHCXML)
		c := testClient(t, s.addr(), WithUDPRetranslate(udp))
		if err := c.Connect(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := refBuildXML("aabbccddeeff", udp)
		if got := s.XMLRequest(); got != want {
			t.Errorf("udp=%v:\n получено %q\n эталон   %q", udp, got, want)
		}
		c.Close()
	}
}

// bigLogic собирает правдоподобный logic.xml, заведомо больше одного чтения
// из сокета: так проверяется, что логика вычитывается потоком целиком.
func bigLogic(items int) []byte {
	var b bytes.Buffer
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<shc>\n")
	for i := 0; i < items; i++ {
		fmt.Fprintf(&b, "<item addr=\"%d:1\" cfgid=\"%d\" name=\"Комната %d\" type=\"lamp\"/>\n", 700+i, i, i)
	}
	b.WriteString("</shc>\n")
	return b.Bytes()
}

// TestConnectDeliversLogic проверяет, что logic.xml доезжает до приёмника
// байт в байт.
func TestConnectDeliversLogic(t *testing.T) {
	logic := bigLogic(2000)
	s := newFakeServerLogic(t, 0x10, tagSHCXML, logic)

	var sink bytes.Buffer
	c := testClient(t, s.addr(), WithLogicSink(&sink))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if got := sink.Bytes(); !bytes.Equal(got, logic) {
		t.Errorf("в приёмник пришло %d байт, ожидалось %d", len(got), len(logic))
	}
}

// TestConnectWithoutLogicSinkKeepsFrames проверяет, что без приёмника большая
// логика по-прежнему отбрасывается целиком и не сбивает границы кадров:
// следующий же пакет должен уехать на провод неискажённым.
func TestConnectWithoutLogicSinkKeepsFrames(t *testing.T) {
	const serverByte = 0x10
	s := newFakeServerLogic(t, serverByte, tagSHCXML, bigLogic(2000))
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Send(context.Background(), Byte(773, 1, 1)); err != nil {
		t.Fatal(err)
	}

	want := refPackReceiveData(initClientDefValue-uint16(serverByte), 773, 1, PDSetStatusToServer, 1, []byte{1})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Received()) >= len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Received(); !bytes.Equal(got, want) {
		t.Errorf("после пропуска логики на провод ушло:\n получено %#v\n ожидалось %#v", got, want)
	}
}

var errSinkBroken = errors.New("приёмник сломан")

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errSinkBroken }

// TestConnectFailsOnBrokenLogicSink: молча потерять логику, которую у нас
// попросили, хуже, чем не подключиться.
func TestConnectFailsOnBrokenLogicSink(t *testing.T) {
	s := newFakeServerLogic(t, 0x10, tagSHCXML, bigLogic(100))
	c := testClient(t, s.addr(), WithLogicSink(brokenWriter{}))

	err := c.Connect(context.Background())
	if !errors.Is(err, errSinkBroken) {
		t.Fatalf("получено %v, ожидалась ошибка приёмника", err)
	}
	if c.Connected() {
		t.Error("клиент считает себя подключённым после ошибки приёмника")
	}
}

func TestConnectInvalidKeyRejectedByServer(t *testing.T) {
	s := newFakeServer(t, 0, tagPKFail)
	c := testClient(t, s.addr())

	err := c.Connect(context.Background())
	if !errors.Is(err, ErrInvalidServerKey) {
		t.Errorf("получено %v, ожидалась ErrInvalidServerKey", err)
	}
	if c.Connected() {
		t.Error("клиент считает себя подключённым после отказа сервера")
	}
}

// TestConnectSkipsUnknownPackets проверяет, что клиент пропускает чужие
// пакеты и всё равно доходит до shcxml.
func TestConnectSkipsUnknownPackets(t *testing.T) {
	const serverByte = 0x05
	s := newFakeServer(t, serverByte, tagSHCXML, "junkaa", "junkbb")
	c := testClient(t, s.addr())

	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if got, want := c.ClientID(), initClientDefValue-uint16(serverByte); got != want {
		t.Errorf("client id = %d, ожидался %d", got, want)
	}
}

func TestConnectTwice(t *testing.T) {
	s := newFakeServer(t, 1, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.Connect(context.Background()); !errors.Is(err, ErrAlreadyConnected) {
		t.Errorf("повторный Connect вернул %v", err)
	}
}

func TestSendWritesExactBytes(t *testing.T) {
	const serverByte = 0x10
	s := newFakeServer(t, serverByte, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	temp, err := Sensor(100, 0, 23.5)
	if err != nil {
		t.Fatal(err)
	}
	text, err := Text(100, 1, "норма")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), temp, text); err != nil {
		t.Fatal(err)
	}

	clientID := initClientDefValue - uint16(serverByte)
	want := append(
		refPackSensor(clientID, 100, 0, 23.5),
		refPackStatusText(clientID, 100, 1, "норма")...,
	)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Received()) >= len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Received(); !bytes.Equal(got, want) {
		t.Errorf("на провод ушло:\n получено %#v\n ожидалось %#v", got, want)
	}
}

// TestRequestAllWritesExactBytes проверяет раскладку запроса состояний:
// PD=14, нулевые id и subid, пустая нагрузка и — главное — нулевой client id
// вместо полученного при рукопожатии.
func TestRequestAllWritesExactBytes(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.RequestAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	want := refPackReceiveData(0, 0, 0, PDRequestAllDevices, 0, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.Received()) >= len(want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := s.Received(); !bytes.Equal(got, want) {
		t.Errorf("на провод ушло:\n получено %#v\n ожидалось %#v", got, want)
	}
	if c.ClientID() == 0 {
		t.Fatal("client id рукопожатия равен нулю — тест не отличит его от нуля в пакете")
	}
}

func TestRequestAllWithoutConnect(t *testing.T) {
	c := testClient(t, "127.0.0.1:1")
	if err := c.RequestAll(context.Background()); !errors.Is(err, ErrNotConnected) {
		t.Errorf("получено %v, ожидалась ErrNotConnected", err)
	}
}

func TestSendWithoutConnect(t *testing.T) {
	c := testClient(t, "127.0.0.1:1")
	v, err := Sensor(1, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(context.Background(), v); !errors.Is(err, ErrNotConnected) {
		t.Errorf("получено %v, ожидалась ErrNotConnected", err)
	}
}

func TestSendNoValuesIsNoop(t *testing.T) {
	c := testClient(t, "127.0.0.1:1")
	if err := c.Send(context.Background()); err != nil {
		t.Errorf("пустой Send вернул %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	s := newFakeServer(t, 1, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("первый Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("второй Close: %v", err)
	}
	if c.Connected() {
		t.Error("Connected() = true после Close")
	}
}

// TestConnectCancelled проверяет, что отмена контекста не оставляет клиент
// висеть на чтении challenge, которого сервер не пришлёт.
func TestConnectCancelled(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// Молчим: клиент должен выйти по отмене контекста, а не по таймауту.
		time.Sleep(3 * time.Second)
		conn.Close()
	}()

	c := testClient(t, ln.Addr().String(), WithReadTimeout(30*time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := c.Connect(ctx); err == nil {
		t.Fatal("ожидалась ошибка при отменённом контексте")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Connect висел %v, отмена контекста не сработала", elapsed)
	}
}

// TestConcurrentSend гоняет отправку из нескольких горутин: с -race это
// ловит гонки на записи в сокет.
func TestConcurrentSend(t *testing.T) {
	s := newFakeServer(t, 1, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, err := Sensor(uint16(i), 0, float64(i))
			if err != nil {
				t.Error(err)
				return
			}
			if err := c.Send(context.Background(), v); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
}
