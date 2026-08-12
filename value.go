package shclient

import (
	"encoding/binary"
	"math"
)

// Диапазон значений датчика, представимый протоколом.
//
// Показание передаётся как uint16(v*256) — формат fixed-point 8.8, поэтому
// представить можно только 0 … 65535/256. Всё, что больше, переполняется, а
// отрицательные значения при конверсии float64 → uint16 в Go дают
// implementation-dependent результат. Проверку делает [Sensor].
const (
	SensorMin = 0.0
	SensorMax = 255.99609375 // 65535/256
)

// maxPayload — предел поля длины в заголовке пакета.
const maxPayload = math.MaxUint16

// Value — значение, готовое к отправке. Создаётся конструкторами [Text],
// [Byte], [Raw], [Sensor] и [SensorRaw]; реализовать интерфейс снаружи пакета
// нельзя, чтобы на провод не попали пакеты с неизвестной раскладкой.
type Value interface {
	pack(clientID uint16) []byte
}

// rawValue — полезная нагрузка, которая уходит на провод как есть.
// За ней стоят [Text], [Byte] и [Raw]: они отличаются только тем, как
// собирают байты, и проверками на входе.
type rawValue struct {
	id      uint16
	subID   uint8
	payload []byte
}

func (v rawValue) pack(clientID uint16) []byte {
	return packPacket(clientID, v.id, v.subID, PDSetStatusToServer, v.payload)
}

// sensorValue — показание датчика в формате fixed-point 8.8.
type sensorValue struct {
	id    uint16
	subID uint8
	raw   uint16
}

func (v sensorValue) pack(clientID uint16) []byte {
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, v.raw)
	return packPacket(clientID, v.id, v.subID, PDSetStatusToServer, payload)
}

// Text создаёт текстовый статус для элемента id/subID.
// Возвращает [ErrPayloadTooLarge], если текст не помещается в поле длины.
func Text(id uint16, subID uint8, text string) (Value, error) {
	if len(text) > maxPayload {
		return nil, ErrPayloadTooLarge
	}
	return rawValue{id: id, subID: subID, payload: []byte(text)}, nil
}

// Byte создаёт однобайтовое значение для элемента id/subID.
//
// Часть элементов хранит состояние ровно в одном байте: лампа, скрипт, клапан,
// шторы, ворота, датчики двери и протечки. Показание датчика такому элементу
// слать нельзя: [Sensor] пакует значение в два байта fixed-point 8.8, и
// единица уезжает как 00 01 — младший байт нулевой, элемент остаётся
// выключенным. Выключение при этом внешне работает, потому что ноль даёт нули
// в обоих байтах, — отсюда ощущение, что статус пропадает через раз.
//
// Ошибиться тут нечем: один байт помещается в поле длины всегда, поэтому
// ошибка не возвращается.
func Byte(id uint16, subID uint8, v uint8) Value {
	return rawValue{id: id, subID: subID, payload: []byte{v}}
}

// Raw создаёт значение с произвольной полезной нагрузкой: байты уходят на
// провод как есть. Нужен для элементов, чья раскладка не сводится ни к тексту,
// ни к показанию датчика.
//
// Возвращает [ErrPayloadTooLarge], если payload не помещается в поле длины.
// Срез копируется, поэтому его изменение после вызова на отправку не влияет.
func Raw(id uint16, subID uint8, payload []byte) (Value, error) {
	if len(payload) > maxPayload {
		return nil, ErrPayloadTooLarge
	}
	return rawValue{id: id, subID: subID, payload: append([]byte(nil), payload...)}, nil
}

// Sensor создаёт показание датчика для элемента id/subID.
//
// Значение должно лежать в диапазоне [SensorMin, SensorMax]; иначе
// возвращается [ErrValueOutOfRange]. Величины большего масштаба (ватты,
// отрицательные токи) масштабируйте или смещайте перед вызовом.
func Sensor(id uint16, subID uint8, value float64) (Value, error) {
	if math.IsNaN(value) || value < SensorMin || value > SensorMax {
		return nil, ErrValueOutOfRange
	}
	return sensorValue{id: id, subID: subID, raw: uint16(value * 256)}, nil
}

// SensorRaw создаёт показание датчика из уже готового значения fixed-point 8.8.
// Нужен, когда вызывающий код сам управляет масштабом.
func SensorRaw(id uint16, subID uint8, raw uint16) Value {
	return sensorValue{id: id, subID: subID, raw: raw}
}

// ClampSensor приводит значение в допустимый диапазон и сообщает, была ли
// обрезка. Удобно там, где потерять точность лучше, чем потерять пакет,
// но факт обрезки всё равно нужно залогировать.
func ClampSensor(value float64) (clamped float64, wasClamped bool) {
	switch {
	case math.IsNaN(value):
		return SensorMin, true
	case value < SensorMin:
		return SensorMin, true
	case value > SensorMax:
		return SensorMax, true
	default:
		return value, false
	}
}
