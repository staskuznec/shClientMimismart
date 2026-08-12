package shclient

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func TestPackPacketLayout(t *testing.T) {
	got := packPacket(0x0102, 0x0304, 0x05, PDSetStatusToServer, []byte{0xAA, 0xBB})

	want := []byte{
		0x02, 0x01, // client id, little-endian
		0x04, 0x03, // id, little-endian
		0x05,       // PD
		0x00, 0x00, // два нулевых байта
		0x05,       // subid
		0x02, 0x00, // длина полезной нагрузки
		0xAA, 0xBB, // полезная нагрузка
	}
	if !bytes.Equal(got, want) {
		t.Errorf("раскладка пакета:\n получено %#v\n ожидалось %#v", got, want)
	}
	if len(got) != headerSize+2 {
		t.Errorf("длина пакета = %d, ожидалась %d", len(got), headerSize+2)
	}
}

// TestPackEquivalenceSensor доказывает, что новая упаковка датчика совпадает
// байт в байт с эталонной реализацией из internal-версии пакета.
func TestPackEquivalenceSensor(t *testing.T) {
	values := []float64{0, 0.5, 1, 23.5, 100.25, 255, SensorMax}
	ids := []uint16{0, 1, 100, 0x7ef, 65535}
	subs := []uint8{0, 1, 9, 255}
	clientIDs := []uint16{0, 1, 0x7ef, 2031, 65535}

	for _, clientID := range clientIDs {
		for _, id := range ids {
			for _, sub := range subs {
				for _, v := range values {
					value, err := Sensor(id, sub, v)
					if err != nil {
						t.Fatalf("Sensor(%v): %v", v, err)
					}
					got := value.pack(clientID)
					want := refPackSensor(clientID, id, sub, v)
					if !bytes.Equal(got, want) {
						t.Fatalf("датчик id=%d sub=%d v=%v client=%d:\n получено %#v\n эталон   %#v",
							id, sub, v, clientID, got, want)
					}
				}
			}
		}
	}
}

// TestPackEquivalenceText — то же самое для текстового статуса.
func TestPackEquivalenceText(t *testing.T) {
	texts := []string{"", "on", "off", "норма", "23.5°C", string(make([]byte, 300))}
	for _, s := range texts {
		value, err := Text(42, 7, s)
		if err != nil {
			t.Fatalf("Text(%q): %v", s, err)
		}
		got := value.pack(2031)
		want := refPackStatusText(2031, 42, 7, s)
		if !bytes.Equal(got, want) {
			t.Errorf("текст %q:\n получено %#v\n эталон   %#v", s, got, want)
		}
	}
}

func TestSensorRange(t *testing.T) {
	bad := []float64{-0.001, -1, 256, 1000, 2300, math.NaN(), math.Inf(1), math.Inf(-1)}
	for _, v := range bad {
		if _, err := Sensor(1, 0, v); !errors.Is(err, ErrValueOutOfRange) {
			t.Errorf("Sensor(%v) вернул %v, ожидалась ErrValueOutOfRange", v, err)
		}
	}

	good := []float64{SensorMin, 0.00390625, 1, 128, SensorMax}
	for _, v := range good {
		if _, err := Sensor(1, 0, v); err != nil {
			t.Errorf("Sensor(%v) вернул ошибку %v", v, err)
		}
	}
}

// TestSensorMaxIsExact проверяет, что верхняя граница диапазона действительно
// упаковывается в 0xFFFF и не переполняется.
func TestSensorMaxIsExact(t *testing.T) {
	value, err := Sensor(1, 0, SensorMax)
	if err != nil {
		t.Fatal(err)
	}
	pkt := value.pack(0)
	raw := binary.LittleEndian.Uint16(pkt[headerSize:])
	if raw != math.MaxUint16 {
		t.Errorf("SensorMax упакован как %d, ожидалось %d", raw, math.MaxUint16)
	}
}

func TestSensorRaw(t *testing.T) {
	pkt := SensorRaw(10, 2, 0xBEEF).pack(1)
	raw := binary.LittleEndian.Uint16(pkt[headerSize:])
	if raw != 0xBEEF {
		t.Errorf("raw = %#x, ожидалось 0xBEEF", raw)
	}
}

func TestClampSensor(t *testing.T) {
	tests := []struct {
		in      float64
		want    float64
		clamped bool
	}{
		{100, 100, false},
		{-5, SensorMin, true},
		{2300, SensorMax, true},
		{math.NaN(), SensorMin, true},
		{SensorMax, SensorMax, false},
	}
	for _, tt := range tests {
		got, clamped := ClampSensor(tt.in)
		if got != tt.want || clamped != tt.clamped {
			t.Errorf("ClampSensor(%v) = (%v, %v), ожидалось (%v, %v)",
				tt.in, got, clamped, tt.want, tt.clamped)
		}
	}
}

func TestTextTooLarge(t *testing.T) {
	if _, err := Text(1, 0, string(make([]byte, maxPayload+1))); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("ожидалась ErrPayloadTooLarge, получено %v", err)
	}
}

// TestPackEquivalenceByte сверяет однобайтовое значение с эталонной упаковкой:
// тот же PD, длина 1, полезная нагрузка из одного байта.
func TestPackEquivalenceByte(t *testing.T) {
	for _, v := range []uint8{0, 1, 8, 9, 0x7F, 0xFF} {
		got := Byte(773, 1, v).pack(2031)
		want := refPackReceiveData(2031, 773, 1, PDSetStatusToServer, 1, []byte{v})
		if !bytes.Equal(got, want) {
			t.Errorf("Byte(%#x):\n получено %#v\n эталон   %#v", v, got, want)
		}
	}
}

// TestByteVsSensorForUnitValue фиксирует ту самую разницу, ради которой
// появился Byte: у датчика единица уезжает как 00 01, и однобайтовый элемент
// читает из неё ноль.
func TestByteVsSensorForUnitValue(t *testing.T) {
	sensor, err := Sensor(773, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	sensorPkt := sensor.pack(0)
	if got := sensorPkt[headerSize:]; !bytes.Equal(got, []byte{0x00, 0x01}) {
		t.Fatalf("Sensor(1) упакован как %#v, ожидалось 00 01", got)
	}

	bytePkt := Byte(773, 1, 1).pack(0)
	if got := bytePkt[headerSize:]; !bytes.Equal(got, []byte{0x01}) {
		t.Errorf("Byte(1) упакован как %#v, ожидался один байт 01", got)
	}
	if got := binary.LittleEndian.Uint16(bytePkt[8:10]); got != 1 {
		t.Errorf("длина в заголовке = %d, ожидалась 1", got)
	}
}

func TestPackEquivalenceRaw(t *testing.T) {
	payloads := [][]byte{{}, {0xFF}, {0x01, 0x02, 0x03}, bytes.Repeat([]byte{0xAB}, 300)}
	for _, p := range payloads {
		value, err := Raw(42, 7, p)
		if err != nil {
			t.Fatalf("Raw(%d байт): %v", len(p), err)
		}
		got := value.pack(2031)
		want := refPackReceiveData(2031, 42, 7, PDSetStatusToServer, uint16(len(p)), p)
		if !bytes.Equal(got, want) {
			t.Errorf("Raw(%d байт):\n получено %#v\n эталон   %#v", len(p), got, want)
		}
	}
}

func TestRawTooLarge(t *testing.T) {
	if _, err := Raw(1, 0, make([]byte, maxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("ожидалась ErrPayloadTooLarge, получено %v", err)
	}
	if _, err := Raw(1, 0, make([]byte, maxPayload)); err != nil {
		t.Errorf("payload ровно в предел отвергнут: %v", err)
	}
}

// TestRawCopiesPayload проверяет, что значение не держит ссылку на срез
// вызывающего: переиспользование буфера не должно менять то, что уедет.
func TestRawCopiesPayload(t *testing.T) {
	buf := []byte{0x01, 0x02}
	value, err := Raw(1, 0, buf)
	if err != nil {
		t.Fatal(err)
	}
	buf[0] = 0xFF

	if got := value.pack(0)[headerSize:]; !bytes.Equal(got, []byte{0x01, 0x02}) {
		t.Errorf("полезная нагрузка = %#v, ожидалось 01 02 — срез не скопирован", got)
	}
}
