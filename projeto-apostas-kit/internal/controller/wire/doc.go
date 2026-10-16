// Package wire reúne a leitura dos campos de uma operação de provedor, comum a todas as
// entradas (HTTP e SQS). Assim as duas montam exatamente o mesmo pedido e, portanto, o
// mesmo hash de idempotência.
package wire
