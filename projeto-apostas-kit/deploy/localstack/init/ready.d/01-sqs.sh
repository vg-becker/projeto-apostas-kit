#!/bin/bash
set -euo pipefail

# Provisiona as filas do serviço. Os valores abaixo são o contrato: os testes de integração
# montam este mesmo script no LocalStack e conferem cada um deles.
#
#   wager-transactions.fifo      operações dos provedores (VisibilityTimeout 60 s, redrive
#                                para a DLQ depois de 5 recebimentos)
#   wager-transactions-dlq.fifo  mensagens que não podem ser processadas
#   wager-events.fifo            destino dos eventos de saída (MessageGroupId = aggregateId,
#                                MessageDeduplicationId = eventId)

awslocal sqs create-queue \
  --queue-name wager-transactions-dlq.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false

DLQ_ARN=$(awslocal sqs get-queue-attributes \
  --queue-url http://localhost:4566/000000000000/wager-transactions-dlq.fifo \
  --attribute-names QueueArn \
  --query 'Attributes.QueueArn' \
  --output text)

# A política restringe o acesso à fila à conta confiável. O LocalStack Community a guarda,
# mas não a aplica; em produção, o controle de quem publica é do IAM e desta política.
cat > /tmp/wager-transactions-attributes.json <<JSON
{
  "FifoQueue": "true",
  "ContentBasedDeduplication": "false",
  "VisibilityTimeout": "60",
  "RedrivePolicy": "{\"deadLetterTargetArn\":\"${DLQ_ARN}\",\"maxReceiveCount\":\"5\"}",
  "Policy": "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Sid\":\"TrustedAccountOnly\",\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::000000000000:root\"},\"Action\":[\"sqs:SendMessage\",\"sqs:ReceiveMessage\",\"sqs:DeleteMessage\",\"sqs:ChangeMessageVisibility\",\"sqs:GetQueueAttributes\"],\"Resource\":\"*\"}]}"
}
JSON

awslocal sqs create-queue \
  --queue-name wager-transactions.fifo \
  --attributes file:///tmp/wager-transactions-attributes.json

awslocal sqs create-queue \
  --queue-name wager-events.fifo \
  --attributes FifoQueue=true,ContentBasedDeduplication=false
