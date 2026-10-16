package ledger

import (
	"fmt"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

// EntryParams reúne os dados de um lançamento, novo ou reidratado.
type EntryParams struct {
	ID            identity.EntryID
	WalletID      identity.WalletID
	TransactionID identity.TransactionID
	Direction     Direction
	Money         money.Money
	BalanceBefore money.Money
	BalanceAfter  money.Money
	CreatedAt     time.Time
}

// Entry é um lançamento imutável do ledger; correções exigem novos lançamentos.
type Entry struct {
	id            identity.EntryID
	walletID      identity.WalletID
	transactionID identity.TransactionID
	direction     Direction
	money         money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

// NewEntry cria um lançamento validando balanceAfter = balanceBefore ± money.
func NewEntry(params EntryParams) (Entry, error) {
	return build(params)
}

// Rehydrate reconstrói um lançamento persistido, sem efeitos colaterais.
func Rehydrate(params EntryParams) (Entry, error) {
	return build(params)
}

// ID devolve o identificador do lançamento.
func (entry Entry) ID() identity.EntryID { return entry.id }

// WalletID devolve a carteira movimentada.
func (entry Entry) WalletID() identity.WalletID { return entry.walletID }

// TransactionID devolve a transação que originou o lançamento.
func (entry Entry) TransactionID() identity.TransactionID { return entry.transactionID }

// Direction devolve DEBIT ou CREDIT.
func (entry Entry) Direction() Direction { return entry.direction }

// Money devolve o valor movimentado, sempre positivo.
func (entry Entry) Money() money.Money { return entry.money }

// BalanceBefore devolve o saldo anterior ao lançamento.
func (entry Entry) BalanceBefore() money.Money { return entry.balanceBefore }

// BalanceAfter devolve o saldo posterior ao lançamento.
func (entry Entry) BalanceAfter() money.Money { return entry.balanceAfter }

// CreatedAt devolve o instante de criação em UTC.
func (entry Entry) CreatedAt() time.Time { return entry.createdAt }

func build(params EntryParams) (Entry, error) {
	if err := validate(params); err != nil {
		return Entry{}, err
	}

	return Entry{
		id:            params.ID,
		walletID:      params.WalletID,
		transactionID: params.TransactionID,
		direction:     params.Direction,
		money:         params.Money,
		balanceBefore: params.BalanceBefore,
		balanceAfter:  params.BalanceAfter,
		createdAt:     params.CreatedAt.UTC(),
	}, nil
}

func validate(params EntryParams) error {
	if err := validateIdentity(params); err != nil {
		return err
	}

	if err := validateAmounts(params); err != nil {
		return err
	}

	return validateArithmetic(params)
}

func validateIdentity(params EntryParams) error {
	if !params.ID.IsValid() || !params.WalletID.IsValid() || !params.TransactionID.IsValid() {
		return invalid("id, walletId and transactionId must be valid")
	}

	if !params.Direction.isValid() {
		return invalid(fmt.Sprintf("direction %q", params.Direction))
	}

	if params.CreatedAt.IsZero() {
		return invalid("createdAt is required")
	}

	return nil
}

func validateAmounts(params EntryParams) error {
	amounts := []money.Money{params.Money, params.BalanceBefore, params.BalanceAfter}
	for _, amount := range amounts {
		if !amount.IsInitialized() || amount.Currency() != params.Money.Currency() {
			return invalid("money and balances must share one initialized currency")
		}
	}

	if !params.Money.IsPositive() {
		return invalid("money must be positive")
	}

	if params.BalanceBefore.IsNegative() || params.BalanceAfter.IsNegative() {
		return invalid("balances must not be negative")
	}

	return nil
}

func validateArithmetic(params EntryParams) error {
	expected, err := expectedBalance(params)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBalanceMismatch, err)
	}

	if !expected.Equal(params.BalanceAfter) {
		return fmt.Errorf(
			"%w: expected %s, got %s",
			ErrBalanceMismatch,
			expected,
			params.BalanceAfter,
		)
	}

	return nil
}

func expectedBalance(params EntryParams) (money.Money, error) {
	if params.Direction == Credit {
		return params.BalanceBefore.Add(params.Money)
	}

	return params.BalanceBefore.Sub(params.Money)
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidEntry, reason)
}
