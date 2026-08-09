package shclient

import "encoding/binary"

// Коды команд протокола (PD).
const (
	PDRequestAllDevices   uint8 = 4  // запрос всех устройств
	PDSetStatusToServer   uint8 = 5  // установка статуса на сервер
	PDSetStatusFromServer uint8 = 7  // получение статуса от сервера
	PDPingModule          uint8 = 15 // пинг модуля с состояниями
)

// Размеры элементов протокола.
const (
	headerSize    = 10 // заголовок пакета значения
	challengeSize = 16 // challenge авторизации, ровно один блок AES
	replyTagSize  = 6  // текстовый тег в ответе сервера
	replyHeadSize = 4 + replyTagSize
	crcByteSize   = 5 // 4 байта CRC + 1 байт сервера
)

// initClientDefValue — база, из которой вычисляется идентификатор клиента:
// clientID = initClientDefValue - serverByte.
const initClientDefValue uint16 = 0x7ef

// Теги ответов сервера на XML-запрос.
const (
	tagSHCXML = "shcxml"
	tagPKFail = "pkfail"
)

// handshakeAttempts — сколько пакетов от сервера просматривается в поисках
// shcxml, прежде чем сдаться.
const handshakeAttempts = 3

// packPacket собирает пакет значения: 10-байтовый заголовок и полезная
// нагрузка. Все многобайтовые поля — little-endian.
func packPacket(clientID, id uint16, subID, pd uint8, payload []byte) []byte {
	buf := make([]byte, headerSize+len(payload))
	binary.LittleEndian.PutUint16(buf[0:2], clientID)
	binary.LittleEndian.PutUint16(buf[2:4], id)
	buf[4] = pd
	buf[5] = 0
	buf[6] = 0
	buf[7] = subID
	binary.LittleEndian.PutUint16(buf[8:10], uint16(len(payload)))
	copy(buf[headerSize:], payload)
	return buf
}
