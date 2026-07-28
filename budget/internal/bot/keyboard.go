package bot

import (
	"strconv"

	tele "gopkg.in/telebot.v3"

	"budget/internal/classify"
	"budget/internal/storage"
)

// Уникальные имена коллбэков. Данные — id транзакции, при выборе категории
// после двоеточия добавляется id категории.
const (
	cbPayer    = "ben_payer"
	cbPartner  = "ben_partner"
	cbBoth     = "ben_both"
	cbCategory = "cat_open"
	cbPick     = "cat_pick"
	cbDelete   = "tx_delete"
)

// transactionKeyboard — клавиатура под записанной тратой (§9).
//
// Показываются только те варианты, которых сейчас нет: нажатие на уже
// выбранный ничего не меняет, Telegram отвечает «message is not modified»,
// и человек видит, что кнопка «не работает».
func transactionKeyboard(txID int64, beneficiary, partner string) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	id := strconv.FormatInt(txID, 10)

	var choices []tele.Btn
	for _, b := range []struct {
		value, label, unique string
	}{
		{classify.BenPayer, "себе", cbPayer},
		{classify.BenPartner, partnerLabel(partner), cbPartner},
		{classify.BenBoth, "на двоих", cbBoth},
	} {
		if b.value != beneficiary {
			choices = append(choices, m.Data(b.label, b.unique, id))
		}
	}

	m.Inline(
		m.Row(choices...),
		m.Row(
			m.Data("категория", cbCategory, id),
			m.Data("удалить", cbDelete, id),
		),
	)
	return m
}

// transferKeyboard — у перевода менять нечего, кроме факта его существования.
func transferKeyboard(txID int64) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	m.Inline(m.Row(m.Data("удалить", cbDelete, strconv.FormatInt(txID, 10))))
	return m
}

// categoryKeyboard — сетка категорий по две в ряд (§9).
func categoryKeyboard(txID int64, cats []storage.Category) *tele.ReplyMarkup {
	m := &tele.ReplyMarkup{}
	id := strconv.FormatInt(txID, 10)

	var rows []tele.Row
	var row []tele.Btn
	for _, c := range cats {
		row = append(row, m.Data(c.Name, cbPick, id+":"+strconv.FormatInt(int64(c.ID), 10)))
		if len(row) == 2 {
			rows = append(rows, m.Row(row...))
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, m.Row(row...))
	}
	m.Inline(rows...)
	return m
}

// keyboardFor выбирает клавиатуру по типу операции.
func keyboardFor(t storage.Transaction, partner string) *tele.ReplyMarkup {
	if t.Kind == classify.KindTransfer {
		return transferKeyboard(t.ID)
	}
	return transactionKeyboard(t.ID, t.Beneficiary, partner)
}
