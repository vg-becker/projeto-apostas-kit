package wallet

import (
	"fmt"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/ledger"
	"apostas-go/internal/model/money"
)

const initialVersion = 1

// Wallet é a raiz do agregado financeiro. O saldo só muda por Debit e Credit,
// que devolvem o lançamento da movimentação; o lock e a transação são do repositório.
type Wallet struct {
	id        identity.WalletID
	playerID  identity.PlayerID
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// Snapshot é o estado persistido de uma carteira.
type Snapshot struct {
	ID        identity.WalletID
	PlayerID  identity.PlayerID
	Balance   money.Money
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OpenParams reúne os dados da abertura de uma carteira.
type OpenParams struct {
	ID                   identity.WalletID
	PlayerID             identity.PlayerID
	InitialBalance       money.Money
	OpeningTransactionID identity.TransactionID
	OpeningEntryID       identity.EntryID
	Now                  time.Time
}

// Opened é o resultado da abertura; OpeningEntry é nulo quando o saldo inicial é zero.
type Opened struct {
	Wallet       *Wallet
	OpeningEntry *ledger.Entry
}

// MovementParams reúne os dados de um débito ou crédito.
type MovementParams struct {
	TransactionID identity.TransactionID
	EntryID       identity.EntryID
	Amount        money.Money
	Now           time.Time
}

// Open cria a carteira com versão 1. Com saldo inicial positivo, devolve também o
// lançamento de abertura, que não incrementa a versão.
func Open(params OpenParams) (Opened, error) {
	wallet, err := Rehydrate(Snapshot{
		ID:        params.ID,
		PlayerID:  params.PlayerID,
		Balance:   params.InitialBalance,
		Version:   initialVersion,
		CreatedAt: params.Now,
		UpdatedAt: params.Now,
	})
	if err != nil {
		return Opened{}, err
	}

	if params.InitialBalance.IsZero() {
		return Opened{Wallet: wallet}, nil
	}

	entry, err := openingEntry(params)
	if err != nil {
		return Opened{}, err
	}

	return Opened{Wallet: wallet, OpeningEntry: &entry}, nil
}

// Rehydrate reconstrói uma carteira persistida validando as invariantes,
// sem reaplicar movimentações nem emitir eventos.
func Rehydrate(snapshot Snapshot) (*Wallet, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}

	return &Wallet{
		id:        snapshot.ID,
		playerID:  snapshot.PlayerID,
		balance:   snapshot.Balance,
		version:   snapshot.Version,
		createdAt: snapshot.CreatedAt.UTC(),
		updatedAt: snapshot.UpdatedAt.UTC(),
	}, nil
}

// Debit retira o valor do saldo e devolve o lançamento DEBIT dessa movimentação.
func (wallet *Wallet) Debit(params MovementParams) (ledger.Entry, error) {
	if err := wallet.validateMovement(params.Amount); err != nil {
		return ledger.Entry{}, err
	}

	after, err := wallet.balance.Sub(params.Amount)
	if err != nil {
		return ledger.Entry{}, err
	}

	if after.IsNegative() {
		return ledger.Entry{}, fmt.Errorf(
			"%w: balance %s, debit %s",
			ErrInsufficientFunds,
			wallet.balance,
			params.Amount,
		)
	}

	return wallet.apply(ledger.Debit, params, after)
}

// Credit soma o valor ao saldo e devolve o lançamento CREDIT dessa movimentação.
func (wallet *Wallet) Credit(params MovementParams) (ledger.Entry, error) {
	if err := wallet.validateMovement(params.Amount); err != nil {
		return ledger.Entry{}, err
	}

	after, err := wallet.balance.Add(params.Amount)
	if err != nil {
		return ledger.Entry{}, err
	}

	return wallet.apply(ledger.Credit, params, after)
}

// Snapshot devolve uma cópia do estado para persistência.
func (wallet *Wallet) Snapshot() Snapshot {
	return Snapshot{
		ID:        wallet.id,
		PlayerID:  wallet.playerID,
		Balance:   wallet.balance,
		Version:   wallet.version,
		CreatedAt: wallet.createdAt,
		UpdatedAt: wallet.updatedAt,
	}
}

// ID devolve o identificador da carteira.
func (wallet *Wallet) ID() identity.WalletID { return wallet.id }

// PlayerID devolve o jogador dono da carteira.
func (wallet *Wallet) PlayerID() identity.PlayerID { return wallet.playerID }

// Balance devolve o saldo atual.
func (wallet *Wallet) Balance() money.Money { return wallet.balance }

// Currency devolve a moeda da carteira.
func (wallet *Wallet) Currency() money.Currency { return wallet.balance.Currency() }

// Version devolve a versão, incrementada a cada mudança de saldo.
func (wallet *Wallet) Version() int64 { return wallet.version }

// CreatedAt devolve o instante de criação em UTC.
func (wallet *Wallet) CreatedAt() time.Time { return wallet.createdAt }

// UpdatedAt devolve o instante da última mudança de saldo em UTC.
func (wallet *Wallet) UpdatedAt() time.Time { return wallet.updatedAt }

func (wallet *Wallet) validateMovement(amount money.Money) error {
	if !amount.IsInitialized() {
		return money.ErrUninitialized
	}

	if amount.Currency() != wallet.balance.Currency() {
		return fmt.Errorf(
			"%w: wallet %s, movement %s",
			ErrCurrencyMismatch,
			wallet.balance.Currency(),
			amount.Currency(),
		)
	}

	if !amount.IsPositive() {
		return fmt.Errorf("%w: %s", ErrAmountNotPositive, amount)
	}

	return nil
}

// apply cria o lançamento antes de mudar o estado: se ele for inválido, nada muda.
func (wallet *Wallet) apply(
	direction ledger.Direction,
	params MovementParams,
	after money.Money,
) (ledger.Entry, error) {
	entry, err := ledger.NewEntry(ledger.EntryParams{
		ID:            params.EntryID,
		WalletID:      wallet.id,
		TransactionID: params.TransactionID,
		Direction:     direction,
		Money:         params.Amount,
		BalanceBefore: wallet.balance,
		BalanceAfter:  after,
		CreatedAt:     params.Now,
	})
	if err != nil {
		return ledger.Entry{}, err
	}

	wallet.balance = after
	wallet.version++
	wallet.updatedAt = params.Now.UTC()

	return entry, nil
}

func openingEntry(params OpenParams) (ledger.Entry, error) {
	zero, err := money.Zero(params.InitialBalance.Currency())
	if err != nil {
		return ledger.Entry{}, err
	}

	return ledger.NewEntry(ledger.EntryParams{
		ID:            params.OpeningEntryID,
		WalletID:      params.ID,
		TransactionID: params.OpeningTransactionID,
		Direction:     ledger.Credit,
		Money:         params.InitialBalance,
		BalanceBefore: zero,
		BalanceAfter:  params.InitialBalance,
		CreatedAt:     params.Now,
	})
}

func validateSnapshot(snapshot Snapshot) error {
	if !snapshot.ID.IsValid() || !snapshot.PlayerID.IsValid() {
		return invalidState("id and playerId must be valid")
	}

	if !snapshot.Balance.IsInitialized() {
		return invalidState("balance must be initialized")
	}

	if snapshot.Balance.IsNegative() {
		return invalidState("balance must not be negative")
	}

	return validateVersionAndTimes(snapshot)
}

func validateVersionAndTimes(snapshot Snapshot) error {
	if snapshot.Version < initialVersion {
		return invalidState("version must be at least 1")
	}

	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return invalidState("createdAt is required and updatedAt must not precede it")
	}

	return nil
}

func invalidState(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, reason)
}
