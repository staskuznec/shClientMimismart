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
// [Sensor] и [SensorRaw]; реализовать интерфейс снаружи пакета нельзя, чтобы
// на провод не попали пакеты с неизвестной раскладкой.
type Value interface {
	pack(clientID uint16) []byte
}

// textValue — текстовый статус.
type textValue struct {
	id      uint16
	subID   uint8
	payload []byte
}

func (v textValue) pack(clientID uint16) []byte {
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
	return textValue{id: id, subID: subID, payload: []byte(text)}, nil
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
