# shclient

[![Go Reference](https://pkg.go.dev/badge/github.com/staskuznec/shClientMimismart.svg)](https://pkg.go.dev/github.com/staskuznec/shClientMimismart)

Go-клиент протокола SHS умного дома **MimiSmart**: подключение по TCP,
авторизация по ключу AES и отправка значений — текстовых статусов и показаний
датчиков.

Зависимостей нет, только стандартная библиотека.

## Установка

```bash
go get github.com/staskuznec/shClientMimismart
```

## Использование

```go
package main

import (
	"context"
	"log"
	"time"

	shclient "github.com/staskuznec/shClientMimismart"
)

func main() {
	c, err := shclient.New("192.168.1.10:9001", "0123456789abcdef")
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := c.Connect(ctx); err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	temp, err := shclient.Sensor(100, 0, 23.5)
	if err != nil {
		log.Fatal(err)
	}
	status, err := shclient.Text(100, 1, "норма")
	if err != nil {
		log.Fatal(err)
	}

	if err := c.Send(ctx, temp, status); err != nil {
		log.Fatal(err)
	}
}
```

`Send` принимает любое число значений и отправляет их одним батчем: между
пакетами выдерживается пауза, а после отправки клиент вычитывает ответы
сервера — без этого сервер может закрыть соединение, решив, что клиент его не
слушает.

## Диапазон значений датчиков

Это главная особенность протокола, о которую легко споткнуться. Показание
датчика передаётся как **fixed-point 8.8**: `uint16(v * 256)`. Представить
можно только диапазон:

```
SensorMin = 0
SensorMax = 255.99609375   // 65535/256
```

Всё, что выходит за эти границы, представить нельзя. Поэтому `Sensor`
возвращает `ErrValueOutOfRange`, а не пакует молча испорченное значение:
мощность 2300 Вт при наивной упаковке превратилась бы в 3520, а отрицательный
ток — в число около 65000 (конверсия отрицательного float в uint16 в Go
implementation-dependent).

Величины большего масштаба нужно приводить к диапазону на своей стороне:

```go
// мощность в десятках ватт: 2300 Вт → 230
v, err := shclient.Sensor(100, 1, watts*0.1)

// либо обрезать явно, если потеря точности допустима
value, clamped := shclient.ClampSensor(watts)
if clamped {
	log.Warn("значение обрезано", "raw", watts)
}
```

Если масштабом управляет вызывающий код, есть прямой путь:

```go
shclient.SensorRaw(100, 1, raw) // готовое значение fixed-point 8.8
```

## Настройки

```go
c, err := shclient.New(addr, key,
	shclient.WithLogger(slog.Default()),        // по умолчанию логов нет
	shclient.WithDialTimeout(10*time.Second),
	shclient.WithReadTimeout(15*time.Second),
	shclient.WithWriteTimeout(10*time.Second),
	shclient.WithPacketDelay(10*time.Millisecond),
	shclient.WithAutoDrain(true),
	shclient.WithDrainTimings(200*time.Millisecond, 500*time.Millisecond),
	shclient.WithUDPRetranslate(true),          // атрибут retranslate-udp
	shclient.WithMacID("aabbccddeeff"),         // пусто — crypto/rand
)
```

Все сетевые операции уважают отмену контекста: `ctx` прерывает уже начатое
чтение или запись, а не только ожидание перед ними.

## Тестирование приложений

Для подмены клиента заглушкой есть узкий интерфейс:

```go
type Sender interface {
	Send(ctx context.Context, values ...Value) error
}
```

## Формат обмена

Соединение начинается с авторизации: сервер присылает 16 байт challenge,
клиент шифрует их одним блоком AES на ключе и отправляет обратно. Затем клиент
шлёт XML-запрос `get-shc` и получает ответ `shcxml`, из которого вычисляется
идентификатор клиента, проставляемый во все дальнейшие пакеты.

Пакет значения — 10-байтовый заголовок и полезная нагрузка, все многобайтовые
поля little-endian:

| Смещение | Размер | Поле                    |
|----------|--------|-------------------------|
| 0        | 2      | client id               |
| 2        | 2      | id элемента             |
| 4        | 1      | PD (код команды)        |
| 5        | 1      | 0                       |
| 6        | 1      | 0                       |
| 7        | 1      | subid                   |
| 8        | 2      | длина полезной нагрузки |
| 10       | N      | полезная нагрузка       |

Тесты сверяют упаковку байт в байт с эталонной реализацией
(`reference_test.go`), поэтому формат на проводе гарантированно совпадает с
предыдущей internal-версией пакета.

## Разработка

```bash
go vet ./...
go test -race ./...
```

## Лицензия

MIT
