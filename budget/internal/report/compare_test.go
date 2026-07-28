package report

import (
	"testing"
	"time"
)

func TestComparableRange(t *testing.T) {
	msk := time.FixedZone("MSK", 3*60*60)
	day := func(y int, m time.Month, d int) time.Time {
		return time.Date(y, m, d, 12, 0, 0, 0, msk)
	}

	cases := []struct {
		name      string
		now       time.Time
		year      int
		month     time.Month
		wantOK    bool
		wantDays  int
		wantSpan  int // сколько дней в отрезке текущего месяца
		wantPart  bool
		wantStart string
	}{
		{
			name: "первые дни месяца не сравниваем", now: day(2026, 7, 2),
			year: 2026, month: time.July, wantOK: false,
		},
		{
			name: "середина июля против середины июня", now: day(2026, 7, 15),
			year: 2026, month: time.July, wantOK: true, wantDays: 15, wantSpan: 15,
			wantPart: true, wantStart: "2026-06-01",
		},
		{
			name: "31 июля обрезается до 30 дней июня", now: day(2026, 7, 31),
			year: 2026, month: time.July, wantOK: true, wantDays: 30, wantSpan: 30,
			wantPart: true, wantStart: "2026-06-01",
		},
		{
			name: "30 марта обрезается до 28 дней февраля", now: day(2026, 3, 30),
			year: 2026, month: time.March, wantOK: true, wantDays: 28, wantSpan: 28,
			wantPart: true, wantStart: "2026-02-01",
		},
		{
			name: "31 декабря обрезается до 30 дней ноября", now: day(2026, 12, 31),
			year: 2026, month: time.December, wantOK: true, wantDays: 30, wantSpan: 30,
			wantPart: true, wantStart: "2026-11-01",
		},
		{
			name: "январь сравнивается с декабрём прошлого года", now: day(2026, 1, 20),
			year: 2026, month: time.January, wantOK: true, wantDays: 20, wantSpan: 20,
			wantPart: true, wantStart: "2025-12-01",
		},
		{
			name: "закрытый месяц целиком", now: day(2026, 8, 5),
			year: 2026, month: time.July, wantOK: true, wantDays: 30, wantSpan: 31,
			wantPart: false, wantStart: "2026-06-01",
		},
		{
			name: "будущий месяц сравнивать не с чем", now: day(2026, 7, 15),
			year: 2026, month: time.September, wantOK: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ComparableRange(c.now, c.year, c.month, msk)
			if ok != c.wantOK {
				t.Fatalf("сравнение возможно = %v, ожидалось %v", ok, c.wantOK)
			}
			if !ok {
				return
			}
			if got.Days != c.wantDays {
				t.Errorf("дней = %d, ожидалось %d", got.Days, c.wantDays)
			}
			if got.Partial != c.wantPart {
				t.Errorf("частичный = %v, ожидалось %v", got.Partial, c.wantPart)
			}
			if start := got.From.Format("2006-01-02"); start != c.wantStart {
				t.Errorf("начало прошлого отрезка = %s, ожидалось %s", start, c.wantStart)
			}
			// Обе стороны сравнения обязаны быть одной длины, иначе дельта
			// систематически завышена.
			prevDays := int(got.To.Sub(got.From).Hours() / 24)
			curDays := int(got.CurrentTo.Sub(got.CurrentFrom).Hours() / 24)
			if got.Partial && prevDays != curDays {
				t.Errorf("отрезки разной длины: прошлый %d, текущий %d", prevDays, curDays)
			}
			if curDays != c.wantSpan {
				t.Errorf("отрезок текущего месяца = %d дней, ожидалось %d", curDays, c.wantSpan)
			}
		})
	}
}

func TestComparableRangeNeverExceedsPreviousMonth(t *testing.T) {
	// Перебором по всем месяцам двух лет: отрезок прошлого месяца не должен
	// вылезать за его границы, иначе в сравнение попадёт чужой месяц.
	msk := time.FixedZone("MSK", 3*60*60)

	for year := 2025; year <= 2026; year++ {
		for month := time.January; month <= time.December; month++ {
			start, end := MonthRange(year, month, msk)
			for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
				got, ok := ComparableRange(d, year, month, msk)
				if !ok {
					continue
				}
				prevStart, prevEnd := MonthRange(year, month, msk)
				prevStart = prevStart.AddDate(0, -1, 0)
				prevEnd = start

				if got.From.Before(prevStart) || got.To.After(prevEnd) {
					t.Fatalf("%s: отрезок %s..%s вылез за прошлый месяц %s..%s",
						d.Format("2006-01-02"),
						got.From.Format("2006-01-02"), got.To.Format("2006-01-02"),
						prevStart.Format("2006-01-02"), prevEnd.Format("2006-01-02"))
				}
			}
		}
	}
}
