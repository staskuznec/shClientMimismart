module github.com/staskuznec/shClientMimismart

go 1.22

// RequestAll в этой версии отправлял нулевой client id вместо полученного
// при рукопожатии — сервер такой запрос игнорирует. Исправлено в v0.3.0.
retract v0.2.0
