//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// Uma instância cai com SIGKILL no meio do tráfego. As mensagens que ela tinha em mãos voltam
// à fila quando a visibilidade vence, os eventos que ela tinha reservado voltam quando o lease
// vence, e as requisições HTTP repetidas em outra instância viram replays: nada se perde,
// nada se duplica.
func TestKillingAnInstanceInTheMiddleOfTheTrafficLosesAndDuplicatesNothing(t *testing.T) {
	const (
		walletCount = 8
		perWallet   = 25
		httpPerWall = 5
		betMinor    = 500
	)

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	wallets := running.openWallets(t, walletCount, "1000.00")

	var traffic sync.WaitGroup

	traffic.Add(2)

	go func() {
		defer traffic.Done()

		running.sendMessages(t, wallets, perWallet)
	}()

	go func() {
		defer traffic.Done()

		running.betOverHTTPWithRetry(t, wallets, httpPerWall, token)
	}()

	eventually(t, 60*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`) >= 20
	})

	running.kill(t, 0)
	running.logWhatTheCrashLeftBehind(t)

	traffic.Wait()

	// a visibilidade das mensagens que a instância morta tinha em mãos vence em até 60 s
	eventually(t, 180*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM inbox_messages WHERE completed_at IS NOT NULL`) == walletCount*perWallet
	})

	running.assertEveryWallet(t, wallets, perWallet+httpPerWall, 100000-(perWallet+httpPerWall)*betMinor)
	running.assertSound(t)
}

func (running *cluster) sendMessages(t *testing.T, wallets []walletRef, perWallet int) {
	t.Helper()

	for index := range perWallet {
		for position, wallet := range wallets {
			external := fmt.Sprintf("sqs-%d-%d", position, index)
			running.environment.sendBet(t, wallet, "msg-"+external, external, "5.00")
		}
	}
}

// betOverHTTPWithRetry aposta por HTTP em todas as instâncias, inclusive a que vai cair; uma
// falha de conexão leva a repetir a operação, com a mesma chave, em outra.
func (running *cluster) betOverHTTPWithRetry(
	t *testing.T,
	wallets []walletRef,
	perWallet int,
	token string,
) {
	t.Helper()

	for index := range perWallet {
		for position, wallet := range wallets {
			external := fmt.Sprintf("http-%d-%d", position, index)
			response := running.submitUntilAnswered(t, position+index, token, "provider-a:"+external,
				operationBody(wallet, "provider-a", external, "5.00"))

			if response.status != http.StatusOK {
				t.Errorf("http bet %s = %d %s", external, response.status, response.body)
			}

			time.Sleep(15 * time.Millisecond)
		}
	}
}

// logWhatTheCrashLeftBehind registra o que a instância morta deixou em andamento, para quem
// ler o resultado do teste saber que a recuperação foi de fato exercitada.
func (running *cluster) logWhatTheCrashLeftBehind(t *testing.T) {
	t.Helper()

	attributes, err := running.environment.client.QueueAttributes(
		t.Context(), running.environment.queues.Requests)
	if err != nil {
		t.Fatalf("queue attributes: %v", err)
	}

	leased := running.number(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND lease_until IS NOT NULL`)

	t.Logf("the crash left %s messages in flight and %d outbox events leased",
		attributes["ApproximateNumberOfMessagesNotVisible"], leased)
}

// Um REFUND ou ROLLBACK que chega antes da aposta que referencia fica PENDING_REFERENCE; o
// worker de alguma instância o conclui quando a aposta aparece em outra.
func TestAReversalArrivingBeforeItsBetIsResolvedAcrossInstances(t *testing.T) {
	const wallets = 6

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	opened := running.openWallets(t, wallets, "1000.00")

	for index, wallet := range opened {
		running.reverseBeforeBet(t, index, wallet, token)
	}

	eventually(t, 90*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM wager_transactions WHERE kind IN ('REFUND', 'ROLLBACK') AND status = 'PROCESSED'`) == wallets
	})

	running.assertEveryWallet(t, opened, 1, 100000)

	waiting := running.number(t, `SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionPendingReference'`)
	resolved := running.number(t, `SELECT count(*) FROM wager_transactions WHERE kind IN ('REFUND', 'ROLLBACK') AND resolved_reference_id IS NOT NULL`)

	if waiting != wallets || resolved != wallets {
		t.Fatalf("pending events = %d, resolved references = %d, want %d each", waiting, resolved, wallets)
	}

	if done := running.resolutionsObserved(t); done != wallets {
		t.Fatalf("the workers observed %v resolutions across the instances, want %d", done, wallets)
	}

	running.assertSound(t)
}

// reverseBeforeBet envia o estorno (HTTP nas carteiras pares, SQS nas ímpares), confere que
// ele ficou à espera e só então envia a aposta, em outra instância.
func (running *cluster) reverseBeforeBet(t *testing.T, index int, wallet walletRef, token string) {
	t.Helper()

	kind, bet, reversal := "REFUND", fmt.Sprintf("bet-%d", index), fmt.Sprintf("reversal-%d", index)
	if index%3 == 0 {
		kind = "ROLLBACK"
	}

	if index%2 == 0 {
		response := running.submitUntilAnswered(t, index, token, "provider-a:"+reversal,
			referencingBody(wallet, reversal, kind, "5.00", bet))
		if response.status != http.StatusAccepted {
			t.Fatalf("reversal %s = %d %s, want 202 (waiting for its reference)", reversal, response.status, response.body)
		}
	} else {
		running.environment.sendReversal(t, wallet, "msg-"+reversal, reversal, kind, bet)
		eventually(t, 30*time.Second, func() bool {
			return running.number(t, `SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1 AND status = 'PENDING_REFERENCE'`, reversal) == 1
		})
	}

	response := running.submitUntilAnswered(t, index+1, token, "provider-a:"+bet,
		operationBody(wallet, "provider-a", bet, "5.00"))
	if response.status != http.StatusOK {
		t.Fatalf("bet %s = %d %s", bet, response.status, response.body)
	}
}

// resolutionsObserved soma, nas métricas de todas as instâncias, as pendências resolvidas
// pelos workers.
func (running *cluster) resolutionsObserved(t *testing.T) float64 {
	t.Helper()

	var total float64

	for index := range running.instances {
		total += sumOf(running.metrics(t, index), `operations_total{`, `source="pending_reference"`, `status="PROCESSED"`)
	}

	return total
}

// Idempotência e saldos sobrevivem à troca de todas as instâncias: as mesmas requisições, depois
// do reinício, são replays; a mesma chave com outro conteúdo continua sendo conflito.
func TestIdempotencyAndBalancesSurviveARestartOfTheWholeCluster(t *testing.T) {
	const (
		walletCount = 8
		perWallet   = 3
	)

	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	wallets := running.openWallets(t, walletCount, "1000.00")

	originals := map[string]string{}

	for position, wallet := range wallets {
		for index := range perWallet {
			external := fmt.Sprintf("tx-%d-%d", position, index)
			response := running.submitUntilAnswered(t, position+index, token, "provider-a:"+external,
				operationBody(wallet, "provider-a", external, "5.00"))

			if response.status != http.StatusOK || response.object(t)["idempotentReplay"] != false {
				t.Fatalf("%s = %d %s", external, response.status, response.body)
			}

			originals[external] = response.object(t)["transactionId"].(string)
		}
	}

	running.restart(t)

	running.assertReplaysAfterRestart(t, wallets, perWallet, token, originals)
	running.assertEveryWallet(t, wallets, perWallet, 100000-perWallet*500)
	running.assertSound(t)
}

func (running *cluster) assertReplaysAfterRestart(
	t *testing.T,
	wallets []walletRef,
	perWallet int,
	token string,
	originals map[string]string,
) {
	t.Helper()

	for position, wallet := range wallets {
		for index := range perWallet {
			external := fmt.Sprintf("tx-%d-%d", position, index)

			running.assertReplayAndConflict(t, position+index, wallet, token, external, originals[external])
		}
	}
}

// assertReplayAndConflict confere, depois do reinício, que repetir a operação devolve o
// resultado original como replay e que a mesma chave com outro valor é um conflito.
func (running *cluster) assertReplayAndConflict(
	t *testing.T,
	instance int,
	wallet walletRef,
	token string,
	external string,
	originalTransaction string,
) {
	t.Helper()

	key := "provider-a:" + external

	replay := running.submitUntilAnswered(t, instance, token, key,
		operationBody(wallet, "provider-a", external, "5.00"))

	if replay.status != http.StatusOK || replay.object(t)["idempotentReplay"] != true ||
		replay.object(t)["transactionId"] != originalTransaction {
		t.Fatalf("%s after the restart = %d %s, want the original result as a replay",
			external, replay.status, replay.body)
	}

	conflict := running.submitUntilAnswered(t, instance+1, token, key,
		operationBody(wallet, "provider-a", external, "6.00"))

	if conflict.status != http.StatusConflict || conflict.code(t) != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("%s with another amount = %d %s, want the idempotency conflict",
			external, conflict.status, conflict.body)
	}
}

// Derrubar todas as instâncias enquanto há eventos reservados (publicação em curso) deixa a
// outbox com leases de instâncias mortas. Depois do vencimento do lease, as instâncias novas
// publicam esses eventos, e o destino continua com cada um uma só vez.
func TestEventsLeasedByDeadPublishersArePublishedOnceByTheNextOnes(t *testing.T) {
	running := newCluster(t, clusterSize)
	token := bearer(t, "provider-a")
	wallets := running.openWallets(t, 6, "1000.00")

	stop := make(chan struct{})

	var traffic sync.WaitGroup

	traffic.Add(1)

	go func() {
		defer traffic.Done()

		running.betUntilStopped(t, wallets, token, stop)
	}()

	if !waitEvery(2*time.Millisecond, 60*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL AND lease_until IS NOT NULL`) > 0
	}) {
		t.Fatal("no event was ever leased: the publishers never ran")
	}

	for index := range running.instances {
		running.kill(t, index)
	}

	close(stop)
	traffic.Wait()

	orphans := running.leasedAndUnpublished(t)
	if len(orphans) == 0 {
		t.Fatal("the publishers published everything before the crash: nothing to recover, rerun")
	}

	t.Logf("the crash left %d events leased by dead publishers", len(orphans))

	running.restart(t)

	// o lease (30 s) vence e as instâncias novas assumem os eventos
	eventually(t, 120*time.Second, func() bool {
		return running.number(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})

	running.assertEveryOrphanWasPublished(t, orphans)
	running.assertSound(t)
}

// betUntilStopped aposta sem parar, sem repetir: uma falha de conexão por causa da queda é
// esperada, e o que importa depois é a integridade, não a contagem.
func (running *cluster) betUntilStopped(
	t *testing.T,
	wallets []walletRef,
	token string,
	stop <-chan struct{},
) {
	t.Helper()

	for index := 0; ; index++ {
		for position, wallet := range wallets {
			select {
			case <-stop:
				return
			default:
			}

			external := fmt.Sprintf("flow-%d-%d", position, index)
			_, _ = running.trySubmit(position+index, token, "provider-a:"+external,
				operationBody(wallet, "provider-a", external, "1.00"))
		}
	}
}

func (running *cluster) leasedAndUnpublished(t *testing.T) []string {
	t.Helper()

	rows, err := running.environment.database.Query(t.Context(),
		`SELECT event_id::text FROM outbox_events WHERE published_at IS NULL AND lease_until IS NOT NULL`)
	if err != nil {
		t.Fatalf("query leased events: %v", err)
	}

	defer rows.Close()

	var identifiers []string

	for rows.Next() {
		var identifier string
		if err := rows.Scan(&identifier); err != nil {
			t.Fatalf("scan: %v", err)
		}

		identifiers = append(identifiers, identifier)
	}

	return identifiers
}

func (running *cluster) assertEveryOrphanWasPublished(t *testing.T, orphans []string) {
	t.Helper()

	for _, identifier := range orphans {
		published := running.number(t, `SELECT count(*) FROM outbox_events WHERE event_id = $1 AND published_at IS NOT NULL`, identifier)
		if published != 1 {
			t.Fatalf("event %s, leased by a dead publisher, was never published", identifier)
		}
	}
}
