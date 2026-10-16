package sqs

import (
	"errors"
	"strings"
	"testing"

	"apostas-go/internal/controller/wire"
	"apostas-go/internal/model/wager"
	"apostas-go/internal/service"
)

const validBody = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func TestTheChallengeExampleIsAccepted(t *testing.T) {
	message, err := Parse(validBody)
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}

	command := message.Command
	if message.MessageID != "msg-123" || command.ProviderID != "provider-a" ||
		command.IdempotencyKey != "provider-a:transaction-123" || command.Kind != wager.Bet ||
		command.Money.Amount() != "25.00" {
		t.Fatalf("message = %+v", message)
	}

	if command.CorrelationID != "msg-123" || command.CausationID != "msg-123" {
		t.Fatalf("the message id must become the correlation and the causation: %+v", command)
	}
}

func reasonOf(t *testing.T, body string) Reason {
	t.Helper()

	_, err := Parse(body)

	var failure *ParseError
	if !errors.As(err, &failure) {
		t.Fatalf("error = %v, want a *ParseError", err)
	}

	return failure.Reason
}

func TestMalformedMessagesAreRefusedWithAStableReason(t *testing.T) {
	replace := func(old string, replacement string) string {
		return strings.Replace(validBody, old, replacement, 1)
	}

	tests := []struct {
		name string
		body string
		want Reason
	}{
		{name: "empty", body: "", want: ReasonInvalidJSON},
		{name: "not json", body: "hello", want: ReasonInvalidJSON},
		{name: "unknown field", body: replace(`"type"`, `"extra": 1, "type"`), want: ReasonInvalidJSON},
		{name: "data after the json", body: validBody + " {}", want: ReasonInvalidJSON},
		{name: "amount as a number", body: replace(`"25.00"`, `25.00`), want: ReasonInvalidJSON},
		{name: "too large", body: validBody + strings.Repeat(" ", 70<<10), want: ReasonTooLarge},
		{name: "wrong type", body: replace("WagerTransactionRequested", "Other"), want: ReasonUnsupportedType},
		{name: "no type", body: replace(`"type": "WagerTransactionRequested",`, ``), want: ReasonUnsupportedType},
		{name: "no message id", body: replace(`"messageId": "msg-123",`, ``), want: ReasonInvalidField},
		{name: "bad timestamp", body: replace("2026-09-08T12:00:00.000Z", "yesterday"), want: ReasonInvalidField},
		{name: "no data", body: `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z"}`, want: ReasonInvalidField},
		{name: "no provider", body: replace(`"providerId": "provider-a",`, ``), want: ReasonInvalidField},
		{name: "no key", body: replace(`"idempotencyKey": "provider-a:transaction-123",`, ``), want: ReasonInvalidField},
		{name: "opening is internal", body: replace(`"kind": "BET"`, `"kind": "OPENING"`), want: ReasonInvalidField},
		{name: "unknown kind", body: replace(`"kind": "BET"`, `"kind": "SPIN"`), want: ReasonInvalidField},
		{name: "negative amount", body: replace(`"25.00"`, `"-1.00"`), want: ReasonInvalidField},
		{name: "three decimals", body: replace(`"25.00"`, `"25.001"`), want: ReasonInvalidField},
		{name: "bad player", body: replace("0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1", "nope"), want: ReasonInvalidField},
		{name: "unknown currency", body: replace(`"BRL"`, `"XXX"`), want: ReasonInvalidField},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reasonOf(t, test.body); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEveryRefusalIsPermanent(t *testing.T) {
	_, err := Parse(strings.Replace(validBody, `"25.00"`, `"-1.00"`, 1))

	if err == nil || service.Classify(err) == service.ClassTransient {
		t.Fatalf("error = %v: a malformed message must never be retried as transient", err)
	}
}

func TestTheSameBusinessPayloadHashesAlikeOnHTTPAndSQS(t *testing.T) {
	fromSQS, err := Parse(strings.Replace(validBody, `"25.00"`, `"25"`, 1))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}

	fromHTTP, err := wire.OperationFields{
		ExternalTransactionID: "transaction-123",
		PlayerID:              "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
		WalletID:              "0192f291-27dd-7d3f-8071-5f8685deef37",
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 &wire.Money{Amount: "25.00", Currency: "BRL"},
	}.Command()
	if err != nil {
		t.Fatalf("Command error = %v", err)
	}

	fromHTTP.ProviderID = "provider-a"
	fromHTTP.IdempotencyKey = "a-different-key-and-metadata"
	fromHTTP.CorrelationID = "corr"

	sqsHash, sqsErr := service.CanonicalHash(fromSQS.Command)
	httpHash, httpErr := service.CanonicalHash(fromHTTP)

	if sqsErr != nil || httpErr != nil || sqsHash != httpHash {
		t.Fatalf("hashes differ: %s vs %s (%v, %v): HTTP and SQS must be equivalent", sqsHash, httpHash, sqsErr, httpErr)
	}
}
