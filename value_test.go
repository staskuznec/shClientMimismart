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
