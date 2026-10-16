//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const clusterSize = 3

func TestFiftyIdenticalRequestsAcrossThreeInstancesDebitOnce(t *testing.T) {
	running := newCluster(t, clusterSize)
	wallet := running.caller(0).openWallet(t, "1000.00")
	token := bearer(t, "provider-a")
	body := operationBody(wallet, "provider-a", "tx-1", "25.00")

	replies := spread(running, 50, func(_ int, target *stack) reply {
		return target.submit(t, token, "provider-a:tx-1", body)
	})

	originals, transactions := 0, map[string]bool{}

	for _, response := range replies {
		status, object := replyOf(response, t)
		if status != http.StatusOK {
			t.Fatalf("response = %d %s", status, response.body)
		}

		transactions[object["transactionId"].(string)] = true

		if object["idempotentReplay"] == false {
			originals++
		}
	}

	if originals != 1 || len(transactions) != 1 {
		t.Fatalf("originals = %d, distinct transactions = %d, want 1 and 1", originals, len(transactions))
	}

	if running.debits(t, wallet.id) != 1 || running.balance(t, wallet.id) != 97500 {
		t.Fatalf("debits = %d, balance = %d", running.debits(t, wallet.id), running.balance(t, wallet.id))
	}

	running.assertSound(t)
}

// Duas apostas distintas de 80,00 sobre 100,00, cada uma em uma instância, várias vezes.
func TestTwoBetsOf80OverABalanceOf100AcrossInstancesLeaveOneDebit(t *testing.T) {
	const rounds = 15

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")

	for round := range rounds {
		wallet := running.caller(round).openWallet(t, "100.00")
		bets := []string{fmt.Sprintf("round-%d-a", round), fmt.Sprintf("round-%d-b", round)}

		statuses := spread(running, len(bets), func(index int, _ *stack) int {
			target := running.caller(round + index)

			return target.submit(t, token, "provider-a:"+bets[index],
				operationBody(wallet, "provider-a", bets[index], "80.00")).status
		})

		counts := map[int]int{}
		for _, status := range statuses {
			counts[status]++
		}

		if counts[http.StatusOK] != 1 || counts[http.StatusUnprocessableEntity] != 1 {
			t.Fatalf("round %d statuses = %v, want one 200 and one 422", round, counts)
		}

		running.assertOneProcessedOneRejected(t, wallet, bets, token, round)
	}

	running.assertSound(t)
}

func (running *cluster) assertOneProcessedOneRejected(
	t *testing.T,
	wallet walletRef,
	bets []string,
	token string,
	round int,
) {
	t.Helper()

	processed := running.number(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'BET' AND status = 'PROCESSED'`, wallet.id)
	rejected := running.number(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND kind = 'BET' AND status = 'REJECTED' AND failure_code = 'INSUFFICIENT_FUNDS'`, wallet.id)

	if processed != 1 || rejected != 1 || running.debits(t, wallet.id) != 1 || running.balance(t, wallet.id) != 2000 {
		t.Fatalf("round %d: processed=%d rejected=%d debits=%d balance=%d",
			round, processed, rejected, running.debits(t, wallet.id), running.balance(t, wallet.id))
	}

	// reenviar nunca altera o resultado: cada reenvio é um replay, em outra instância
	for index, bet := range bets {
		replay := running.caller(round+index+1).submit(t, token, "provider-a:"+bet,
			operationBody(wallet, "provider-a", bet, "80.00"))

		if replay.object(t)["idempotentReplay"] != true && replay.status != http.StatusUnprocessableEntity {
			t.Fatalf("round %d: resend of %s = %d %s", round, bet, replay.status, replay.body)
		}
	}

	if running.debits(t, wallet.id) != 1 || running.balance(t, wallet.id) != 2000 {
		t.Fatalf("round %d: the resends changed the outcome", round)
	}
}

// Muitas carteiras ao mesmo tempo, por HTTP e por SQS, em três instâncias.
func TestDistinctWalletsProgressInParallelAcrossInstancesOverHTTPAndSQS(t *testing.T) {
	const (
		walletCount = 24
		perChannel  = 6
		betMinor    = 500
	)

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	wallets := running.openWallets(t, walletCount, "1000.00")

	var group sync.WaitGroup

	limit := make(chan struct{}, 24)

	for position, wallet := range wallets {
		for bet := range perChannel {
			group.Add(2)

			go func() {
				defer group.Done()

				limit <- struct{}{}
				defer func() { <-limit }()

				running.betOverHTTP(t, position+bet, wallet, token, fmt.Sprintf("http-%d-%d", position, bet))
			}()

			go func() {
				defer group.Done()

				external := fmt.Sprintf("sqs-%d-%d", position, bet)
				running.environment.sendBet(t, wallet, "msg-"+external, external, "5.00")
			}()
		}
	}

	group.Wait()

	eventually(t, 120*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`) == walletCount*perChannel
	})

	running.assertEveryWallet(t, wallets, 2*perChannel, 100000-2*perChannel*betMinor)
	running.assertEveryInstanceServedRequests(t)
	running.assertSound(t)
}

func (running *cluster) openWallets(t *testing.T, count int, amount string) []walletRef {
	t.Helper()

	wallets := make([]walletRef, count)
	for index := range wallets {
		wallets[index] = running.caller(index).openWallet(t, amount)
	}

	return wallets
}

// betOverHTTP aposta 5,00 pela instância dada (índice cíclico) e espera um 200.
func (running *cluster) betOverHTTP(
	t *testing.T,
	instance int,
	wallet walletRef,
	token string,
	external string,
) {
	t.Helper()

	response := running.caller(instance).submit(t, token, "provider-a:"+external,
		operationBody(wallet, "provider-a", external, "5.00"))

	if response.status != http.StatusOK {
		t.Errorf("http bet %s = %d %s", external, response.status, response.body)
	}
}

func (running *cluster) assertEveryWallet(
	t *testing.T,
	wallets []walletRef,
	wantDebits int,
	wantBalance int,
) {
	t.Helper()

	for _, wallet := range wallets {
		debits, balance := running.debits(t, wallet.id), running.balance(t, wallet.id)
		if debits != wantDebits || balance != wantBalance {
			t.Fatalf("wallet %s: debits = %d, balance = %d, want %d and %d",
				wallet.id, debits, balance, wantDebits, wantBalance)
		}
	}
}

// assertEveryInstanceServedRequests confere, pelas métricas de cada processo, que todos
// participaram: o trabalho foi de fato distribuído.
func (running *cluster) assertEveryInstanceServedRequests(t *testing.T) {
	t.Helper()

	for index := range running.instances {
		if served := sumOf(running.metrics(t, index), `operations_total{`, `source="request"`); served == 0 {
			t.Fatalf("instance %d processed no request: the load was not distributed", index)
		}
	}
}

// sumOf soma as amostras de uma métrica cujas linhas contenham todos os trechos dados.
func sumOf(metrics string, prefix string, contains ...string) float64 {
	var total float64

	for _, line := range strings.Split(metrics, "\n") {
		if !strings.HasPrefix(line, prefix) || !containsAll(line, contains) {
			continue
		}

		fields := strings.Fields(line)
		if value, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			total += value
		}
	}

	return total
}

func containsAll(text string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}

	return true
}

// A mesma operação por HTTP e por três mensagens SQS, ao mesmo tempo, em instâncias diferentes.
func TestTheSameOperationOverHTTPAndSQSAcrossInstancesIsAppliedOnce(t *testing.T) {
	const (
		walletCount = 10
		messages    = 3
	)

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	wallets := running.openWallets(t, walletCount, "1000.00")

	var group sync.WaitGroup

	for position, wallet := range wallets {
		external := fmt.Sprintf("shared-%d", position)

		group.Add(1 + messages)

		go func() {
			defer group.Done()

			running.betOverHTTP(t, position, wallet, token, external)
		}()

		for copyNumber := range messages {
			go func() {
				defer group.Done()

				messageID := fmt.Sprintf("msg-%s-%d", external, copyNumber)
				running.environment.sendBet(t, wallet, messageID, external, "5.00")
			}()
		}
	}

	group.Wait()

	eventually(t, 120*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`) == walletCount*messages
	})

	running.assertEveryWallet(t, wallets, 1, 99500)
	running.assertSound(t)
}
