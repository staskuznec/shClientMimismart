package shclient

import (
	"encoding/binary"
	"time"
)

// Event — то, что сервер прислал клиенту: состояние элемента или нажатие
// в интерфейсе умного дома.
//
// Что означает конкретное значение Payload — вопрос типа элемента, а не
// протокола, и остаётся на стороне вызывающего кода.
type Event struct {
	// SenderID и SenderSubID — адрес элемента, от которого пришло событие.
	SenderID    uint16
	SenderSubID uint8

	// PD — код команды: [PDSetStatusFromServer] для состояния одного
	// элемента, [PDPingModule] для состояния из ответа модуля.
	PD uint8

	// Payload — полезная нагрузка как есть. Срез принадлежит событию,
	// с другими событиями не разделяется и после возврата из обработчика
	// не переиспользуется.
	Payload []byte

	// At — момент, когда пакет был вычитан из сокета.
	At time.Time
}

// inHeader — заголовок входящего пакета.
//
// Раскладка та же, что у исходящего, просто поля читаются в другую сторону:
// байты 5 и 6, которые при отправке всегда нули, в приёме несут номер
// транзакции и subid отправителя.
//
//	смещение  размер  поле
//	0         2       id отправителя
//	2         2       id получателя
//	4         1       PD (код команды)
//	5         1       trans id
//	6         1       subid отправителя
//	7         1       subid получателя
//	8         2       длина полезной нагрузки
type inHeader struct {
	senderID    uint16
	destID      uint16
	pd          uint8
	transID     uint8
	senderSubID uint8
	destSubID   uint8
	length      uint16
}

// parseInHeader разбирает 10 байт заголовка. Длина buf не проверяется:
// вызывающий читает ровно headerSize байт.
func parseInHeader(buf []byte) inHeader {
	return inHeader{
		senderID:    binary.LittleEndian.Uint16(buf[0:2]),
		destID:      binary.LittleEndian.Uint16(buf[2:4]),
		pd:          buf[4],
		transID:     buf[5],
		senderSubID: buf[6],
		destSubID:   buf[7],
		length:      binary.LittleEndian.Uint16(buf[8:10]),
	}
}

// moduleState — одна запись из ответа модуля: состояние одного его элемента.
type moduleState struct {
	subID   uint8
	payload []byte
}

// splitModuleStates разбирает полезную нагрузку пакета [PDPingModule] на
// записи вида subid | length | payload, идущие подряд до конца буфера.
// Идентификатор модуля в записях не повторяется — он берётся из заголовка.
//
// Второе возвращаемое значение — сколько байт осталось неразобранными.
// Ноль означает, что нагрузка разошлась на записи ровно; всё остальное —
// обрезанный хвост, о котором стоит сообщить, но из-за которого не стоит
// терять уже разобранные записи: границы кадров он не сдвигает, длина пакета
// известна из заголовка.
func splitModuleStates(payload []byte) (states []moduleState, tail int) {
	for i := 0; i < len(payload); {
		// Заголовок записи — два байта; если их нет, дальше только хвост.
		if i+2 > len(payload) {
			return states, len(payload) - i
		}
		subID := payload[i]
		n := int(payload[i+1])
		i += 2

		if i+n > len(payload) {
			// Запись объявила больше байт, чем осталось: разбирать нечего,
			// весь остаток вместе с её заголовком считаем хвостом.
			return states, len(payload) - i + 2
		}
		states = append(states, moduleState{subID: subID, payload: payload[i : i+n]})
		i += n
	}
	return states, 0
}
