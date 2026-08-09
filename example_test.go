package shclient_test

import (
	"fmt"

	shclient "github.com/staskuznec/shClientMimismart"
)

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
