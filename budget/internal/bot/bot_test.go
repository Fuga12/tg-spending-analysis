package bot

import (
	"strconv"
	"testing"

	tele "gopkg.in/telebot.v3"
)

func TestParseCommandHandlesCyrillic(t *testing.T) {
	// telebot разбирает команды регуляркой с \w, под которую кириллица не
	// подходит: «/помощь» до обработчика не доезжает и попадает в OnText,
	// где записалось бы тратой. Поэтому разбор свой — см. dispatchCommand.
	cases := []struct {
		in      string
		name    string
		payload string
	}{
		{"/помощь", "/помощь", ""},
		{"/помощь ", "/помощь", ""},
		{"/start", "/start", ""},
		{"/START", "/start", ""},
		{"/помощь@budget_bot", "/помощь", ""},
		{"/месяц@budget_bot 6", "/месяц", "6"},
		{"/месяц 6", "/месяц", "6"},
		{"  /start  ", "/start", ""},
		{"/день 28.07 и ещё", "/день", "28.07 и ещё"},
	}
	for _, c := range cases {
		name, payload := parseCommand(c.in)
		if name != c.name || payload != c.payload {
			t.Errorf("parseCommand(%q) = (%q, %q), ожидалось (%q, %q)",
				c.in, name, payload, c.name, c.payload)
		}
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		user tele.User
		want string
		note string
	}{
		{tele.User{FirstName: "Илья", LastName: "Петров"}, "Илья Петров", "имя и фамилия"},
		{tele.User{FirstName: "Аня"}, "Аня", "только имя"},
		{tele.User{LastName: "Петров"}, "Петров", "только фамилия"},
		{tele.User{Username: "ilya"}, "ilya", "имени нет — берём логин"},
		{tele.User{}, "Без имени", "в Telegram может не быть вообще ничего"},
	}
	for _, c := range cases {
		u := c.user
		if got := displayName(&u); got != c.want {
			t.Errorf("%s: displayName = %q, ожидалось %q", c.note, got, c.want)
		}
	}
}

func TestTxMarkupCarriesTransactionID(t *testing.T) {
	// Кнопка знает только id: группу обработчик берёт у того, кто нажал,
	// а не из callback (см. callbackTarget).
	m := txMarkup(4242)

	if len(m.InlineKeyboard) != 1 || len(m.InlineKeyboard[0]) != 1 {
		t.Fatalf("клавиатура = %+v, ожидалась одна кнопка", m.InlineKeyboard)
	}
	btn := m.InlineKeyboard[0][0]
	if btn.Unique != btnDelete.Unique {
		t.Errorf("кнопка = %q, ожидалось удаление (%q)", btn.Unique, btnDelete.Unique)
	}
	if btn.Data != strconv.Itoa(4242) {
		t.Errorf("данные кнопки = %q, ожидался id траты", btn.Data)
	}
}

func TestUndoMarkupOffersRestore(t *testing.T) {
	// Удаление мягкое, и промах пальцем не должен стоить записи.
	m := undoMarkup(7)

	btn := m.InlineKeyboard[0][0]
	if btn.Unique != btnRestore.Unique || btn.Data != "7" {
		t.Errorf("кнопка = %+v, ожидался возврат траты 7", btn)
	}
}

func TestAppMarkupOnlyWhenAppExists(t *testing.T) {
	// Кнопка, ведущая в никуда, хуже её отсутствия.
	if m := appMarkup(""); m != nil {
		t.Errorf("без APP_URL клавиатура = %+v, ожидалась пустая", m)
	}

	m := appMarkup("https://budget.example.com")
	if m == nil {
		t.Fatal("с APP_URL кнопка должна быть")
	}
	btn := m.InlineKeyboard[0][0]
	if btn.WebApp == nil || btn.WebApp.URL != "https://budget.example.com" {
		t.Errorf("кнопка = %+v, ожидалась ссылка на Mini App", btn)
	}
}

func TestSendOptionsSkipsNilMarkup(t *testing.T) {
	// telebot принимает nil-клавиатуру за обычный аргумент и падает на ней,
	// поэтому её надо не передавать вовсе.
	if opts := sendOptions(nil); len(opts) != 0 {
		t.Errorf("аргументы = %+v, ожидались пустые", opts)
	}
	if opts := sendOptions(txMarkup(1)); len(opts) != 1 {
		t.Errorf("аргументы = %+v, ожидался один", opts)
	}
}

func TestNoGroupAdvisesWhatIsReachable(t *testing.T) {
	// Пока приложения нет, посылать создавать группу туда — тупик.
	withApp := noGroup(true)
	without := noGroup(false)

	if !contains(withApp, "приложении") {
		t.Errorf("с приложением текст = %q, в нём должно быть про приложение", withApp)
	}
	if contains(without, "приложении") {
		t.Errorf("без приложения текст = %q, звать туда некуда", without)
	}
	if !contains(without, "администратора") {
		t.Errorf("без приложения текст = %q, остаётся только администратор", without)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
