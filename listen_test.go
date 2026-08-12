package shclient

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

// TestListenDeliversStatusFromServer — состояние одного элемента (PD=7):
// адрес берётся из id и subid отправителя, полезная нагрузка уходит целиком.
func TestListenDeliversStatusFromServer(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	wait := listenInBackground(t, c, ctx, got.handle)

	s.push(inFrame(773, 2031, PDSetStatusFromServer, 0, 1, 0, []byte{0xFF}))

	events := got.wait(1, 2*time.Second)
	if len(events) != 1 {
		t.Fatalf("получено %d событий, ожидалось 1", len(events))
	}
	e := events[0]
	if e.SenderID != 773 || e.SenderSubID != 1 {
		t.Errorf("адрес события = %d:%d, ожидался 773:1", e.SenderID, e.SenderSubID)
	}
	if e.PD != PDSetStatusFromServer {
		t.Errorf("PD = %d, ожидался %d", e.PD, PDSetStatusFromServer)
	}
	if !bytes.Equal(e.Payload, []byte{0xFF}) {
		t.Errorf("нагрузка = %#v, ожидалось FF", e.Payload)
	}
	if e.At.IsZero() {
		t.Error("время события не проставлено")
	}

	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Listen вернул %v, ожидалась отмена контекста", err)
	}
}

// TestListenSplitsModuleStates — ответ модуля (PD=15) разбирается на
// отдельные элементы, id модуля берётся из заголовка.
func TestListenSplitsModuleStates(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	listenInBackground(t, c, ctx, got.handle)

	// Три элемента модуля 542: однобайтовый, двухбайтовый и пустой.
	payload := []byte{
		1, 1, 0x09,
		16, 2, 0x80, 0x17,
		7, 0,
	}
	s.push(inFrame(542, 2031, PDPingModule, 0, 0, 0, payload))

	events := got.wait(3, 2*time.Second)
	if len(events) != 3 {
		t.Fatalf("получено %d событий, ожидалось 3: %+v", len(events), events)
	}

	want := []struct {
		subID   uint8
		payload []byte
	}{
		{1, []byte{0x09}},
		{16, []byte{0x80, 0x17}},
		{7, []byte{}},
	}
	for i, w := range want {
		e := events[i]
		if e.SenderID != 542 {
			t.Errorf("событие %d: id модуля = %d, ожидался 542", i, e.SenderID)
		}
		if e.SenderSubID != w.subID {
			t.Errorf("событие %d: subid = %d, ожидался %d", i, e.SenderSubID, w.subID)
		}
		if e.PD != PDPingModule {
			t.Errorf("событие %d: PD = %d, ожидался %d", i, e.PD, PDPingModule)
		}
		if !bytes.Equal(e.Payload, w.payload) {
			t.Errorf("событие %d: нагрузка = %#v, ожидалось %#v", i, e.Payload, w.payload)
		}
	}
}

// TestListenPreservesOrder — пакеты не теряются и не переупорядочиваются.
func TestListenPreservesOrder(t *testing.T) {
	const count = 50

	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	listenInBackground(t, c, ctx, got.handle)

	var stream []byte
	for i := 0; i < count; i++ {
		stream = append(stream, inFrame(uint16(i), 2031, PDSetStatusFromServer, 0, 0, 0, []byte{byte(i)})...)
	}
	s.push(stream)

	events := got.wait(count, 3*time.Second)
	if len(events) != count {
		t.Fatalf("получено %d событий, ожидалось %d", len(events), count)
	}
	for i, e := range events {
		if e.SenderID != uint16(i) || !bytes.Equal(e.Payload, []byte{byte(i)}) {
			t.Fatalf("событие %d пришло как id=%d нагрузка=%#v — порядок нарушен",
				i, e.SenderID, e.Payload)
		}
	}
}

// TestSendWhileListening — отправка и приём работают одновременно, а
// автоматическое вычитывание при живом слушателе не запускается: иначе оно
// воровало бы у него байты и тормозило Send на время своего таймаута.
func TestSendWhileListening(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr(), WithAutoDrain(true))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	listenInBackground(t, c, ctx, got.handle)

	// Дожидаемся, пока цикл чтения точно займёт сокет.
	deadline := time.Now().Add(2 * time.Second)
	for !c.Listening() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !c.Listening() {
		t.Fatal("Listen не запустился")
	}

	start := time.Now()
	if err := c.Send(context.Background(), Byte(773, 1, 1)); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	// С вычитыванием Send ждал бы settle + drain, то есть больше 700 мс.
	if elapsed > 300*time.Millisecond {
		t.Errorf("Send занял %v — похоже, вычитывание не отключилось при живом Listen", elapsed)
	}

	// И приём при этом продолжает работать.
	s.push(inFrame(773, 2031, PDSetStatusFromServer, 0, 1, 0, []byte{0x01}))
	if events := got.wait(1, 2*time.Second); len(events) != 1 {
		t.Errorf("после Send получено %d событий, ожидалось 1", len(events))
	}
}

func TestListenRejectsSecondCall(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenInBackground(t, c, ctx, func(Event) {})

	deadline := time.Now().Add(2 * time.Second)
	for !c.Listening() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if err := c.Listen(ctx, func(Event) {}); !errors.Is(err, ErrAlreadyListening) {
		t.Errorf("повторный Listen вернул %v, ожидалась ErrAlreadyListening", err)
	}
	if err := c.Drain(ctx, 10*time.Millisecond); !errors.Is(err, ErrAlreadyListening) {
		t.Errorf("Drain при живом Listen вернул %v, ожидалась ErrAlreadyListening", err)
	}
}

func TestListenValidation(t *testing.T) {
	c := testClient(t, "127.0.0.1:1")
	if err := c.Listen(context.Background(), nil); !errors.Is(err, ErrNilHandler) {
		t.Errorf("Listen без обработчика вернул %v", err)
	}
	if err := c.Listen(context.Background(), func(Event) {}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Listen без соединения вернул %v", err)
	}
}

// TestListenReturnsOnServerDisconnect — обрыв связи возвращается ошибкой,
// а не тишиной: на ней вызывающий и строит переподключение.
func TestListenReturnsOnServerDisconnect(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wait := listenInBackground(t, c, context.Background(), func(Event) {})
	s.closeConn()

	err := wait()
	if err == nil {
		t.Fatal("Listen вернул nil после обрыва соединения")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("обрыв выдан за отмену контекста: %v", err)
	}
	if c.Listening() {
		t.Error("Listening() = true после выхода из Listen")
	}
}

func TestListenIdleTimeout(t *testing.T) {
	s := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s.addr(), WithIdleTimeout(100*time.Millisecond))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wait := listenInBackground(t, c, context.Background(), func(Event) {})
	if err := wait(); !errors.Is(err, ErrIdleTimeout) {
		t.Errorf("Listen вернул %v, ожидалась ErrIdleTimeout", err)
	}
}

// TestKeepaliveRepeatsRequestAll проверяет, что при живом Listen клиент сам
// напоминает о себе серверу.
func TestKeepaliveRepeatsRequestAll(t *testing.T) {
	const serverByte = 0x10
	s := newFakeServer(t, serverByte, tagSHCXML)
	c := testClient(t, s.addr(), WithKeepalive(40*time.Millisecond))
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wait := listenInBackground(t, c, ctx, func(Event) {})

	ping := refPackReceiveData(initClientDefValue-uint16(serverByte), 0, 0, PDRequestAllDevices, 0, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Count(s.Received(), ping) >= 3 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := bytes.Count(s.Received(), ping); got < 3 {
		t.Errorf("сервер получил %d пакетов keepalive, ожидалось не меньше 3", got)
	}

	cancel()
	if err := wait(); !errors.Is(err, context.Canceled) {
		t.Errorf("Listen вернул %v, ожидалась отмена контекста", err)
	}
}

// TestListenAfterReconnect — на этом держится весь паттерн переподключения:
// после Close и повторного Connect клиент снова готов слушать.
func TestListenAfterReconnect(t *testing.T) {
	s1 := newFakeServer(t, 0x10, tagSHCXML)
	c := testClient(t, s1.addr())
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}

	wait := listenInBackground(t, c, context.Background(), func(Event) {})
	s1.closeConn()
	if err := wait(); err == nil {
		t.Fatal("Listen вернул nil после обрыва")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close после обрыва: %v", err)
	}

	// Второй сервер на другом порту — клиент создаётся с новым адресом,
	// но нас интересует, что сам объект переживает цикл заново.
	s2 := newFakeServer(t, 0x20, tagSHCXML)
	c2 := testClient(t, s2.addr(), WithMacID(c.MacID()))
	if err := c2.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var got collector
	listenInBackground(t, c2, ctx, got.handle)
	s2.push(inFrame(773, 2031, PDSetStatusFromServer, 0, 1, 0, []byte{0x01}))

	if events := got.wait(1, 2*time.Second); len(events) != 1 {
		t.Errorf("после переподключения получено %d событий, ожидалось 1", len(events))
	}
}
