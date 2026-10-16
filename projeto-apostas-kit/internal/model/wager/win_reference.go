package wager

import "fmt"

// ValidateWinReference valida a referência opcional de um WIN: uma BET processada da mesma
// rodada, com o mesmo provedor, jogador, carteira e moeda. O valor do WIN não precisa
// igualar o da aposta. Uma referência ainda pendente devolve ErrReferencePending, e uma
// que terminou sem sucesso é recusada de imediato, como nas reversões.
func ValidateWinReference(win *Transaction, reference *Transaction) error {
	if win.snapshot.Kind != Win {
		return fmt.Errorf("%w: %s is not a win", ErrInvalidKind, win.snapshot.Kind)
	}

	if err := checkReferenceStatus(reference); err != nil {
		return err
	}

	if !referencesMatch(win.snapshot, reference.snapshot) {
		return reject(
			ReferenceMismatch,
			"provider, reference id, player, wallet, round or currency differ",
		)
	}

	if reference.snapshot.Kind != Bet {
		return reject(
			ReferenceKindInvalid,
			fmt.Sprintf("WIN cannot reference %s", reference.snapshot.Kind),
		)
	}

	return nil
}
