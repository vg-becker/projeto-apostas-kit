package ledger

import "fmt"

// Direction indica se o lançamento debita ou credita a carteira.
type Direction string

// Direções válidas de um lançamento.
const (
	Debit  Direction = "DEBIT"
	Credit Direction = "CREDIT"
)

// ParseDirection valida a direção recebida de uma fronteira externa.
func ParseDirection(text string) (Direction, error) {
	direction := Direction(text)
	if !direction.isValid() {
		return "", fmt.Errorf("%w: direction %q", ErrInvalidEntry, text)
	}

	return direction, nil
}

// String devolve o texto da direção.
func (direction Direction) String() string {
	return string(direction)
}

func (direction Direction) isValid() bool {
	return direction == Debit || direction == Credit
}
