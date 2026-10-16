package service

import "apostas-go/internal/model/wager"

// AccessScope identifica quem consulta, a partir do token já validado pelo Controller:
// um provedor (só vê o que é seu) ou o serviço interno (vê tudo). O escopo vazio não
// permite nada.
type AccessScope struct {
	ProviderID string
	Internal   bool
}

// ProviderScope é o escopo de um provedor autenticado.
func ProviderScope(providerID string) AccessScope {
	return AccessScope{ProviderID: providerID}
}

// InternalScope é o escopo do serviço interno.
func InternalScope() AccessScope {
	return AccessScope{Internal: true}
}

func (scope AccessScope) valid() bool {
	return scope.Internal || scope.ProviderID != ""
}

// canSee informa se a transação pertence ao escopo. A OPENING, que não tem provedor, só é
// visível ao escopo interno.
func (scope AccessScope) canSee(transaction *wager.Transaction) bool {
	if scope.Internal {
		return true
	}

	return transaction.ProviderID() != "" && transaction.ProviderID() == scope.ProviderID
}
