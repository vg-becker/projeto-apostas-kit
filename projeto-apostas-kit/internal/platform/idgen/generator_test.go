package idgen

import "testing"

func TestGeneratedIDsAreValidV7(t *testing.T) {
	generator := Generator{}

	wallet, walletErr := generator.NewWalletID()
	transaction, transactionErr := generator.NewTransactionID()
	entry, entryErr := generator.NewEntryID()
	event, eventErr := generator.NewEventID()

	for _, err := range []error{walletErr, transactionErr, entryErr, eventErr} {
		if err != nil {
			t.Fatalf("generate error = %v", err)
		}
	}

	if !wallet.IsValid() || !transaction.IsValid() || !entry.IsValid() || !event.IsValid() {
		t.Fatal("generated ids must be canonical UUIDs")
	}

	for _, text := range []string{wallet.String(), transaction.String(), entry.String()} {
		if text[14] != '7' {
			t.Fatalf("%s is not a version 7 uuid", text)
		}
	}
}

func TestGeneratedIDsAreUniqueAndIncreasing(t *testing.T) {
	generator := Generator{}
	previous := ""

	for range 1000 {
		id, err := generator.NewTransactionID()
		if err != nil {
			t.Fatalf("error = %v", err)
		}

		if id.String() <= previous {
			t.Fatalf("%s is not greater than %s", id, previous)
		}

		previous = id.String()
	}
}
