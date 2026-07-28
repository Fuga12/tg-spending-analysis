package storage

import "testing"

func TestBeneficiaryForResolvesPerson(t *testing.T) {
	const ilya, ulya = int64(1), int64(2)
	target := ulya

	// Адресат-человек не зависит от того, кто платил: косметика достаётся
	// Уле, кто бы её ни купил.
	cat := Category{DefaultBeneficiary: BenPayer, DefaultUserID: &target}
	if got := cat.BeneficiaryFor(ilya); got != BenPartner {
		t.Errorf("платит Илья: %q, ожидалось partner — трата на Улю", got)
	}
	if got := cat.BeneficiaryFor(ulya); got != BenPayer {
		t.Errorf("платит Уля: %q, ожидалось payer — трата на себя", got)
	}

	// Без адресата работает прежнее относительное умолчание.
	plain := Category{DefaultBeneficiary: "both"}
	if got := plain.BeneficiaryFor(ilya); got != "both" {
		t.Errorf("без адресата = %q, ожидалось both", got)
	}
}
