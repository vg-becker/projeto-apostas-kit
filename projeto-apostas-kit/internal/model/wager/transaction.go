package wager

import (
	"fmt"
	"strings"
	"time"

	"apostas-go/internal/model/identity"
	"apostas-go/internal/model/money"
)

const (
	maxTextLength = 128
	hashLength    = 64
)

// Snapshot é o estado persistido de uma transação. Para OPENING, os campos
// externos (provedor, IDs externos, chave, hash, rodada, jogo, referência) ficam vazios.
type Snapshot struct {
	ID                             identity.TransactionID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       identity.WalletID
	PlayerID                       identity.PlayerID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	ResolvedReferenceID            identity.TransactionID
	Status                         Status
	FailureCode                    FailureCode
	ResultBalance                  money.Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

// ExternalParams reúne os dados de uma operação recebida de um provedor.
type ExternalParams struct {
	ID                             identity.TransactionID
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       identity.WalletID
	PlayerID                       identity.PlayerID
	RoundID                        string
	GameID                         string
	Kind                           Kind
	Money                          money.Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

// OpeningParams reúne os dados da abertura interna de uma carteira.
type OpeningParams struct {
	ID       identity.TransactionID
	WalletID identity.WalletID
	PlayerID identity.PlayerID
	Money    money.Money
	Now      time.Time
}

// ProcessedResult é o desfecho de uma operação concluída com sucesso.
type ProcessedResult struct {
	Balance             money.Money
	ResolvedReferenceID identity.TransactionID
}

// Transaction é a WagerTransaction; o estado só muda pelos métodos de transição.
type Transaction struct {
	snapshot Snapshot
}

// NewExternal cria uma operação de provedor em PENDING.
func NewExternal(params ExternalParams) (*Transaction, error) {
	if params.Kind == Opening {
		return nil, ErrOpeningNotAllowed
	}

	return Rehydrate(Snapshot{
		ID:                             params.ID,
		ProviderID:                     params.ProviderID,
		ExternalTransactionID:          params.ExternalTransactionID,
		IdempotencyKey:                 params.IdempotencyKey,
		PayloadHash:                    params.PayloadHash,
		WalletID:                       params.WalletID,
		PlayerID:                       params.PlayerID,
		RoundID:                        params.RoundID,
		GameID:                         params.GameID,
		Kind:                           params.Kind,
		Money:                          params.Money,
		ReferenceExternalTransactionID: params.ReferenceExternalTransactionID,
		Status:                         Pending,
		CreatedAt:                      params.Now,
		UpdatedAt:                      params.Now,
	})
}

// NewOpening cria a transação interna de abertura, já em PROCESSED.
func NewOpening(params OpeningParams) (*Transaction, error) {
	return Rehydrate(Snapshot{
		ID:            params.ID,
		WalletID:      params.WalletID,
		PlayerID:      params.PlayerID,
		Kind:          Opening,
		Money:         params.Money,
		Status:        Processed,
		ResultBalance: params.Money,
		CreatedAt:     params.Now,
		UpdatedAt:     params.Now,
	})
}

// Rehydrate reconstrói uma transação persistida validando a coerência,
// sem aplicar transições nem emitir eventos.
func Rehydrate(snapshot Snapshot) (*Transaction, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}

	snapshot.CreatedAt = snapshot.CreatedAt.UTC()
	snapshot.UpdatedAt = snapshot.UpdatedAt.UTC()

	return &Transaction{snapshot: snapshot}, nil
}

// AwaitReference registra a espera por uma referência ainda indisponível.
func (transaction *Transaction) AwaitReference(now time.Time) error {
	if transaction.snapshot.ReferenceExternalTransactionID == "" {
		return fmt.Errorf("%w: transaction has no reference", ErrInvalidTransition)
	}

	if err := transaction.checkTransition(PendingReference, now); err != nil {
		return err
	}

	transaction.moveTo(PendingReference, now)

	return nil
}

// MarkProcessed conclui a operação com sucesso, guardando o saldo devolvido ao provedor.
func (transaction *Transaction) MarkProcessed(result ProcessedResult, now time.Time) error {
	if err := transaction.checkTransition(Processed, now); err != nil {
		return err
	}

	if err := transaction.validateResult(result); err != nil {
		return err
	}

	transaction.snapshot.ResultBalance = result.Balance
	transaction.snapshot.ResolvedReferenceID = result.ResolvedReferenceID
	transaction.moveTo(Processed, now)

	return nil
}

// Reject recusa a operação por regra de negócio, com um código estável.
func (transaction *Transaction) Reject(code FailureCode, now time.Time) error {
	if err := transaction.checkTransition(Rejected, now); err != nil {
		return err
	}

	if !code.isRejection() {
		return fmt.Errorf("%w: %q cannot reject", ErrInvalidFailureCode, code)
	}

	transaction.snapshot.FailureCode = code
	transaction.moveTo(Rejected, now)

	return nil
}

// Fail registra uma falha permanente de infraestrutura, para auditoria.
func (transaction *Transaction) Fail(now time.Time) error {
	if err := transaction.checkTransition(Failed, now); err != nil {
		return err
	}

	transaction.snapshot.FailureCode = InfrastructureFailure
	transaction.moveTo(Failed, now)

	return nil
}

// Snapshot devolve uma cópia do estado para persistência.
func (transaction *Transaction) Snapshot() Snapshot {
	return transaction.snapshot
}

// ID devolve o identificador interno.
func (transaction *Transaction) ID() identity.TransactionID { return transaction.snapshot.ID }

// Kind devolve o tipo.
func (transaction *Transaction) Kind() Kind { return transaction.snapshot.Kind }

// Status devolve o estado atual.
func (transaction *Transaction) Status() Status { return transaction.snapshot.Status }

// Money devolve o valor da operação.
func (transaction *Transaction) Money() money.Money { return transaction.snapshot.Money }

// WalletID devolve a carteira da operação.
func (transaction *Transaction) WalletID() identity.WalletID {
	return transaction.snapshot.WalletID
}

// PlayerID devolve o jogador da operação.
func (transaction *Transaction) PlayerID() identity.PlayerID {
	return transaction.snapshot.PlayerID
}

// ProviderID devolve o provedor; vazio em OPENING.
func (transaction *Transaction) ProviderID() string { return transaction.snapshot.ProviderID }

// ExternalTransactionID devolve o ID do provedor; vazio em OPENING.
func (transaction *Transaction) ExternalTransactionID() string {
	return transaction.snapshot.ExternalTransactionID
}

// RoundID devolve a rodada; vazio em OPENING.
func (transaction *Transaction) RoundID() string { return transaction.snapshot.RoundID }

// ReferenceExternalTransactionID devolve a referência externa, se houver.
func (transaction *Transaction) ReferenceExternalTransactionID() string {
	return transaction.snapshot.ReferenceExternalTransactionID
}

// ResolvedReferenceID devolve a referência interna resolvida, se houver.
func (transaction *Transaction) ResolvedReferenceID() identity.TransactionID {
	return transaction.snapshot.ResolvedReferenceID
}

// FailureCode devolve o código de rejeição ou falha; vazio nos demais estados.
func (transaction *Transaction) FailureCode() FailureCode {
	return transaction.snapshot.FailureCode
}

// ResultBalance devolve o saldo devolvido ao provedor, para replay.
func (transaction *Transaction) ResultBalance() money.Money {
	return transaction.snapshot.ResultBalance
}

func (transaction *Transaction) checkTransition(next Status, now time.Time) error {
	current := transaction.snapshot.Status
	if current.IsTerminal() {
		return fmt.Errorf("%w: already %s", ErrTerminalState, current)
	}

	if !current.canMoveTo(next) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, current, next)
	}

	if now.Before(transaction.snapshot.UpdatedAt) {
		return fmt.Errorf("%w: time moves backwards", ErrInvalidTransition)
	}

	return nil
}

func (transaction *Transaction) moveTo(next Status, now time.Time) {
	transaction.snapshot.Status = next
	transaction.snapshot.UpdatedAt = now.UTC()
}

func (transaction *Transaction) validateResult(result ProcessedResult) error {
	snapshot := transaction.snapshot
	if !result.Balance.IsInitialized() || result.Balance.Currency() != snapshot.Money.Currency() {
		return invalid("result balance must be initialized in the transaction currency")
	}

	hasReference := snapshot.ReferenceExternalTransactionID != ""
	if hasReference != result.ResolvedReferenceID.IsValid() {
		return invalid("resolved reference is required exactly when a reference was informed")
	}

	return nil
}

func validateSnapshot(snapshot Snapshot) error {
	if err := validateCommon(snapshot); err != nil {
		return err
	}

	if snapshot.Kind == Opening {
		return validateOpening(snapshot)
	}

	if err := validateExternalFields(snapshot); err != nil {
		return err
	}

	return validateState(snapshot)
}

func validateCommon(snapshot Snapshot) error {
	if !snapshot.ID.IsValid() || !snapshot.WalletID.IsValid() || !snapshot.PlayerID.IsValid() {
		return invalid("id, walletId and playerId must be valid")
	}

	if !snapshot.Kind.isValid() || !snapshot.Status.isValid() {
		return invalid("kind and status must be known values")
	}

	if err := snapshot.Kind.ValidateAmount(snapshot.Money); err != nil {
		return err
	}

	return validateTimesAndLinks(snapshot)
}

func validateTimesAndLinks(snapshot Snapshot) error {
	if snapshot.CreatedAt.IsZero() || snapshot.UpdatedAt.Before(snapshot.CreatedAt) {
		return invalid("createdAt is required and updatedAt must not precede it")
	}

	resolved := snapshot.ResolvedReferenceID
	if resolved != "" && !resolved.IsValid() {
		return invalid("resolved reference id must be valid")
	}

	return nil
}

func validateOpening(snapshot Snapshot) error {
	external := []string{
		snapshot.ProviderID,
		snapshot.ExternalTransactionID,
		snapshot.IdempotencyKey,
		snapshot.PayloadHash,
		snapshot.RoundID,
		snapshot.GameID,
		snapshot.ReferenceExternalTransactionID,
	}
	for _, text := range external {
		if text != "" {
			return invalid("OPENING must not carry external metadata")
		}
	}

	if snapshot.Status != Processed || !snapshot.ResultBalance.IsInitialized() {
		return invalid("OPENING must be PROCESSED with a result balance")
	}

	return nil
}

func validateExternalFields(snapshot Snapshot) error {
	required := []string{
		snapshot.ProviderID,
		snapshot.ExternalTransactionID,
		snapshot.IdempotencyKey,
		snapshot.RoundID,
		snapshot.GameID,
	}
	for _, text := range required {
		if !validText(text) {
			return invalid("provider, external id, idempotency key, round and game are required")
		}
	}

	if !validHash(snapshot.PayloadHash) {
		return invalid("payload hash must be a lowercase SHA-256 hex digest")
	}

	return validateReference(snapshot)
}

func validateReference(snapshot Snapshot) error {
	reference := snapshot.ReferenceExternalTransactionID

	switch {
	case snapshot.Kind.RequiresReference() && reference == "":
		return invalid(snapshot.Kind.String() + " requires a reference")
	case !snapshot.Kind.AllowsReference() && reference != "":
		return invalid(snapshot.Kind.String() + " must not carry a reference")
	case reference != "" && !validText(reference):
		return invalid("reference external transaction id is malformed")
	case reference != "" && reference == snapshot.ExternalTransactionID:
		return invalid("a transaction cannot reference itself")
	default:
		return nil
	}
}

func validateState(snapshot Snapshot) error {
	switch snapshot.Status {
	case Pending, PendingReference:
		return validateUnfinished(snapshot)
	case Processed:
		return validateProcessed(snapshot)
	case Rejected:
		return validateRejected(snapshot)
	default:
		return validateFailed(snapshot)
	}
}

func validateUnfinished(snapshot Snapshot) error {
	if snapshot.FailureCode != "" || snapshot.ResultBalance.IsInitialized() {
		return invalid("unfinished transaction must have no failure code or result")
	}

	if snapshot.Status == PendingReference && snapshot.ReferenceExternalTransactionID == "" {
		return invalid("PENDING_REFERENCE requires a reference")
	}

	return nil
}

func validateProcessed(snapshot Snapshot) error {
	balance := snapshot.ResultBalance
	if snapshot.FailureCode != "" || !balance.IsInitialized() {
		return invalid("PROCESSED requires a result balance and no failure code")
	}

	if balance.Currency() != snapshot.Money.Currency() {
		return invalid("result balance currency differs from the transaction")
	}

	hasReference := snapshot.ReferenceExternalTransactionID != ""
	if hasReference != snapshot.ResolvedReferenceID.IsValid() {
		return invalid("PROCESSED requires a resolved reference exactly when one was informed")
	}

	return nil
}

func validateRejected(snapshot Snapshot) error {
	if !snapshot.FailureCode.isRejection() || snapshot.ResultBalance.IsInitialized() {
		return invalid("REJECTED requires a rejection code and no result balance")
	}

	return nil
}

func validateFailed(snapshot Snapshot) error {
	if snapshot.FailureCode != InfrastructureFailure || snapshot.ResultBalance.IsInitialized() {
		return invalid("FAILED requires INFRASTRUCTURE_FAILURE and no result balance")
	}

	return nil
}

func validText(text string) bool {
	return text != "" && len(text) <= maxTextLength && strings.TrimSpace(text) == text
}

func validHash(text string) bool {
	if len(text) != hashLength {
		return false
	}

	for _, character := range text {
		if !isLowerHexadecimal(character) {
			return false
		}
	}

	return true
}

func isLowerHexadecimal(character rune) bool {
	return character >= '0' && character <= '9' || character >= 'a' && character <= 'f'
}

func invalid(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidTransaction, reason)
}
