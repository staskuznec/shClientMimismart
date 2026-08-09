package shclient

import "errors"

var (
	// ErrInvalidKey — ключ не подходит для AES: допустимы 16, 24 или 32 байта.
	ErrInvalidKey = errors.New("shclient: длина ключа должна быть 16, 24 или 32 байта")

	// ErrEmptyAddr — не задан адрес сервера.
	ErrEmptyAddr = errors.New("shclient: не задан адрес сервера")

	// ErrNotConnected — операция требует установленного соединения.
	ErrNotConnected = errors.New("shclient: нет соединения, вызовите Connect")

	// ErrAlreadyConnected — повторный Connect на живом соединении.
	ErrAlreadyConnected = errors.New("shclient: соединение уже установлено")

	// ErrInvalidServerKey — сервер отверг авторизацию (ответ pkfail).
	ErrInvalidServerKey = errors.New("shclient: сервер отверг ключ")

	// ErrHandshakeFailed — сервер не прислал shcxml за отведённое число попыток.
	ErrHandshakeFailed = errors.New("shclient: сервер не прислал shcxml")

	// ErrValueOutOfRange — значение не представимо в fixed-point 8.8.
	// Диапазон задан константами SensorMin и SensorMax.
	ErrValueOutOfRange = errors.New("shclient: значение вне диапазона fixed-point 8.8")

	// ErrPayloadTooLarge — полезная нагрузка не помещается в поле длины uint16.
	ErrPayloadTooLarge = errors.New("shclient: полезная нагрузка больше 65535 байт")
)
