//go:build integration

package sqs_test

import (
	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

func moneyOf(minorUnits int64) (money.Money, error) {
	return money.New(minorUnits, money.BRL)
}

func identityPlayer(text string) identity.PlayerID {
	return identity.PlayerID(text)
}
