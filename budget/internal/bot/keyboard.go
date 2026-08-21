package bot

import (
	"strconv"

	tele "gopkg.in/telebot.v3"
)

// Кнопки под записанной тратой.
//
// «Исправить» здесь нет намеренно: править трату — это выбирать категорию,
// получателей и дату, а такой разговор в чате превращается в дерево из
// десятка нажатий. Это работа приложения. В боте остаётся то, что нужно
// сразу после записи и одним движением: «не то — удалить».
var (
	btnDelete  = &tele.InlineButton{Unique: "tx_del", Text: "Удалить"}
	btnRestore = &tele.InlineButton{Unique: "tx_undo", Text: "Вернуть"}
)

// txMarkup — клавиатура под свежей тратой.
func txMarkup(txID int64) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.InlineKeyboard = [][]tele.InlineButton{{*btnDelete.With(strconv.FormatInt(txID, 10))}}
	return m
}

// undoMarkup — клавиатура под удалённой тратой. Удаление мягкое, и промах
// пальцем не должен стоить записи: без «вернуть» человеку пришлось бы
// набирать трату заново.
func undoMarkup(txID int64) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.InlineKeyboard = [][]tele.InlineButton{{*btnRestore.With(strconv.FormatInt(txID, 10))}}
	return m
}

// appMarkup — кнопка, открывающая Mini App. Nil, если приложения ещё нет.
func appMarkup(url string) *tele.ReplyMarkup {
	if url == "" {
		return nil
	}
	m := &tele.ReplyMarkup{}
	m.InlineKeyboard = [][]tele.InlineButton{{
		{Text: "Открыть бюджет", WebApp: &tele.WebApp{URL: url}},
	}}
	return m
}

// sendOptions собирает аргументы Send: nil-клавиатуру telebot принимает за
// обычный аргумент и падает, поэтому её надо не передавать вовсе.
func sendOptions(markup *tele.ReplyMarkup) []any {
	if markup == nil {
		return nil
	}
	return []any{markup}
}
