package shclient

import "encoding/binary"

// Коды команд протокола (PD).
//
// Значения сверены с эталонными реализациями клиента на PHP и Python.
const (
	PDStartPacket uint8 = 1 // стартовый пакет; в эталонах объявлен, но не используется

	// PDSetStatusToServer — установка состояния элемента (клиент → сервер).
	PDSetStatusToServer uint8 = 5

	// PDSetStatusFromServer — состояние одного элемента (сервер → клиент).
	PDSetStatusFromServer uint8 = 7

	// PDRequestAllDevices — запрос состояний. При id = 0 отвечают все
	// устройства, при id = <модуль> этот модуль отвечает первым.
	// Этим же пакетом эталонный Python-клиент поддерживает соединение живым.
	PDRequestAllDevices uint8 = 14

	// PDPingModule — ответ модуля со всеми состояниями своих элементов
	// (сервер → клиент). Клиент такие пакеты не отправляет.
	PDPingModule uint8 = 15

	PDSynchroTime uint8 = 30 // синхронизация времени; в эталонах не используется
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
