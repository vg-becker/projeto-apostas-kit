package port

import "context"

// TxManager executa o trabalho em uma transação SQL. A transação viaja no context,
// e os repositórios a recuperam de lá; erro do trabalho desfaz tudo.
type TxManager interface {
	WithinTransaction(ctx context.Context, work func(ctx context.Context) error) error
}
