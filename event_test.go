package shclient

import (
	"bytes"
	"testing"
)

// TestParseInHeaderMatchesReference сверяет раскладку входящего заголовка
// с эталонами: в Python это struct.unpack("2H4BH", data), в PHP —
// pack("S2C4S", ...) той же формы.
func TestParseInHeaderMatchesReference(t *testing.T) {
	raw := []byte{
		0x1E, 0x02, // id отправителя = 542
		0xEF, 0x07, // id получателя = 2031
		0x0F,       // PD = 15
		0x2A,       // trans id = 42
		0x10,       // subid отправителя = 16
		0x20,       // subid получателя = 32
		0x03, 0x00, // длина = 3
	}

	got := parseInHeader(raw)
	want := inHeader{
		senderID:    542,
		destID:      2031,
		pd:          PDPingModule,
		transID:     42,
		senderSubID: 16,
		destSubID:   32,
		length:      3,
	}
	if got != want {
		t.Errorf("заголовок разобран как %+v, ожидалось %+v", got, want)
	}
}

// TestInHeaderMatchesOutgoingLayout фиксирует то, из-за чего новой раскладки
// изобретать не пришлось: байты, которые при отправке всегда нули, в приёме
// несут trans id и subid отправителя, остальные поля стоят на тех же местах.
func TestInHeaderMatchesOutgoingLayout(t *testing.T) {
	out := packPacket(0x0102, 0x0304, 0x05, PDSetStatusToServer, []byte{0xAA})

	in := parseInHeader(out)
	if in.senderID != 0x0102 {
		t.Errorf("client id прочитан как %#x", in.senderID)
	}
	if in.destID != 0x0304 {
		t.Errorf("id элемента прочитан как %#x", in.destID)
	}
	if in.pd != PDSetStatusToServer {
		t.Errorf("PD прочитан как %d", in.pd)
	}
	if in.transID != 0 || in.senderSubID != 0 {
		t.Errorf("нулевые байты отправки прочитаны как trans=%d sender_sub=%d", in.transID, in.senderSubID)
	}
	if in.destSubID != 0x05 {
		t.Errorf("subid прочитан как %#x", in.destSubID)
	}
	if in.length != 1 {
		t.Errorf("длина прочитана как %d", in.length)
	}
}

func TestSplitModuleStates(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    []moduleState
		tail    int
	}{
		{
			name:    "пусто",
			payload: nil,
			tail:    0,
		},
		{
			name:    "одна запись",
			payload: []byte{1, 1, 0x09},
			want:    []moduleState{{subID: 1, payload: []byte{0x09}}},
		},
		{
			name:    "несколько записей разной длины",
			payload: []byte{1, 1, 0x09, 16, 2, 0x80, 0x17, 7, 0},
			want: []moduleState{
				{subID: 1, payload: []byte{0x09}},
				{subID: 16, payload: []byte{0x80, 0x17}},
				{subID: 7, payload: []byte{}},
			},
		},
		{
			name:    "запись нулевой длины между непустыми",
			payload: []byte{1, 0, 2, 1, 0xFF},
			want: []moduleState{
				{subID: 1, payload: []byte{}},
				{subID: 2, payload: []byte{0xFF}},
			},
		},
		{
			name:    "оборванный заголовок записи",
			payload: []byte{1, 1, 0x09, 5},
			want:    []moduleState{{subID: 1, payload: []byte{0x09}}},
			tail:    1,
		},
		{
			name:    "запись объявила больше, чем осталось",
			payload: []byte{1, 1, 0x09, 5, 10, 0xAA},
			want:    []moduleState{{subID: 1, payload: []byte{0x09}}},
			tail:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, tail := splitModuleStates(tt.payload)
			if len(got) != len(tt.want) {
				t.Fatalf("разобрано %d записей, ожидалось %d: %+v", len(got), len(tt.want), got)
			}
			for i, w := range tt.want {
				if got[i].subID != w.subID {
					t.Errorf("запись %d: subid = %d, ожидался %d", i, got[i].subID, w.subID)
				}
				if !bytes.Equal(got[i].payload, w.payload) {
					t.Errorf("запись %d: нагрузка = %#v, ожидалось %#v", i, got[i].payload, w.payload)
				}
			}
			if tail != tt.tail {
				t.Errorf("хвост = %d байт, ожидалось %d", tail, tt.tail)
			}
		})
	}
}

// TestSplitModuleStatesNoOverlap проверяет, что записи не наезжают друг на
// друга: каждая смотрит в свой участок общего буфера.
func TestSplitModuleStatesNoOverlap(t *testing.T) {
	payload := []byte{1, 2, 0xAA, 0xBB, 2, 2, 0xCC, 0xDD}

	states, tail := splitModuleStates(payload)
	if tail != 0 || len(states) != 2 {
		t.Fatalf("разобрано %d записей, хвост %d", len(states), tail)
	}
	if !bytes.Equal(states[0].payload, []byte{0xAA, 0xBB}) {
		t.Errorf("первая запись = %#v", states[0].payload)
	}
	if !bytes.Equal(states[1].payload, []byte{0xCC, 0xDD}) {
		t.Errorf("вторая запись = %#v", states[1].payload)
	}
}
