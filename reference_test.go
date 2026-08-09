package shclient

import (
	"bytes"
	"encoding/binary"
)

// Эталонная реализация упаковки — копия кода из internal-версии пакета
// (microart-invertor/internal/transport/api/shClient). Нужна ровно для одного:
// доказать, что новая реализация кладёт на провод байт в байт то же самое.
// В продакшен-коде не используется.

type refReceivePacket struct {
	ClientID uint16
	ID       uint16
	PD       uint8
	Zero1    uint8
	Zero2    uint8
	SubID    uint8
	Length   uint16
}

// refPackReceiveData повторяет packReceiveData из исходного пакета.
func refPackReceiveData(clientID uint16, id uint16, subid uint8, pd uint8, length uint16, status []byte) []byte {
	p := refReceivePacket{
		ClientID: clientID,
		ID:       id,
		PD:       pd,
		Zero1:    0,
		Zero2:    0,
		SubID:    subid,
		Length:   length,
	}

	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.LittleEndian, &p); err != nil {
		panic(err)
	}
	for i := 0; i < len(status); i++ {
		if err := binary.Write(buf, binary.LittleEndian, status[i]); err != nil {
			panic(err)
		}
	}
	return buf.Bytes()
}

// refPackStatusText повторяет PackStatusText из исходного пакета.
func refPackStatusText(clientID uint16, id uint16, subid uint8, text string) []byte {
	payload := []byte(text)
	return refPackReceiveData(clientID, id, subid, 5, uint16(len(payload)), payload)
}

// refPackSensor повторяет PackSensor из исходного пакета.
func refPackSensor(clientID uint16, id uint16, subid uint8, floatValue float64) []byte {
	raw := uint16(floatValue * 256)
	payload := make([]byte, 2)
	binary.LittleEndian.PutUint16(payload, raw)
	return refPackReceiveData(clientID, id, subid, 5, uint16(len(payload)), payload)
}

// refBuildXML повторит формирование XML-запроса из handlerSHXML.
func refBuildXML(macID string, retranslateUDP bool) string {
	xml := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<smart-house-commands>\n"
	if retranslateUDP {
		xml += "<get-shc retranslate-udp=\"yes\" mac-id=\"" + macID + "\"/>\n"
	} else {
		xml += "<get-shc mac-id=\"" + macID + "\"/>\n"
	}
	xml += "</smart-house-commands>\n"
	return xml
}
