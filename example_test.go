package shclient_test

import (
	"context"
	"fmt"
	"log"
	"time"

	shclient "github.com/staskuznec/shClientMimismart"
)

// Значения собираются конструкторами и уходят одним батчем в Send.
// Каждое значение адресуется своей парой id/subid — это независимые адреса,
// а не основной и запасной.
func ExampleClient_Send() {
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

	// Ошибка здесь означает «значение не упаковывается», а не проблему сети.
	temp, err := shclient.Sensor(100, 0, 23.5) // элемент 100, подэлемент 0
	if err != nil {
		log.Fatal(err)
	}
	note, err := shclient.Text(100, 1, "норма") // элемент 100, подэлемент 1
	if err != nil {
		log.Fatal(err)
	}

	// А вот ошибка здесь — уже про передачу.
	if err := c.Send(ctx, temp, note); err != nil {
		log.Fatal(err)
	}
}

// Когда значений много, ошибки сборки удобнее обрабатывать по одной,
// не смешивая их с ошибкой отправки.
func ExampleClient_Send_batch() {
	var c *shclient.Client // получен через New и Connect
	ctx := context.Background()

	readings := []struct {
		ID    uint16
		SubID uint8
		Value float64
	}{
		{100, 0, 1},    // реле включено
		{100, 1, 23.0}, // мощность в десятках ватт
		{101, 0, 0},    // реле выключено
	}

	values := make([]shclient.Value, 0, len(readings))
	for _, r := range readings {
		v, err := shclient.Sensor(r.ID, r.SubID, r.Value)
		if err != nil {
			log.Printf("пропускаю %d:%d — %v", r.ID, r.SubID, err)
			continue // одно кривое значение не должно рушить весь батч
		}
		values = append(values, v)
	}

	if err := c.Send(ctx, values...); err != nil {
		log.Fatal(err)
	}
}

// Показание датчика проверяется на попадание в диапазон fixed-point 8.8.
func ExampleSensor() {
	if _, err := shclient.Sensor(100, 0, 23.5); err != nil {
		fmt.Println("23.5:", err)
	} else {
		fmt.Println("23.5: ок")
	}

	// Мощность в ваттах в диапазон не помещается.
	if _, err := shclient.Sensor(100, 1, 2300); err != nil {
		fmt.Println("2300:", err)
	}

	// Тот же ватт, но в десятках ватт — помещается.
	if _, err := shclient.Sensor(100, 1, 2300*0.1); err != nil {
		fmt.Println("230:", err)
	} else {
		fmt.Println("230: ок")
	}

	// Output:
	// 23.5: ок
	// 2300: shclient: значение вне диапазона fixed-point 8.8
	// 230: ок
}

// ClampSensor обрезает значение и сообщает об этом, когда потеря точности
// допустимее потери пакета.
func ExampleClampSensor() {
	value, clamped := shclient.ClampSensor(2300)
	fmt.Printf("%.5f %v\n", value, clamped)

	value, clamped = shclient.ClampSensor(23.5)
	fmt.Printf("%.1f %v\n", value, clamped)

	// Output:
	// 255.99609 true
	// 23.5 false
}
