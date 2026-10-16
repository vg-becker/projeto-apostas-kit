package sqsclient

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

const (
	maxBatch          = 10
	maxLongPollSecond = 20

	receiveCountAttribute = string(types.MessageSystemAttributeNameApproximateReceiveCount)
	groupIDAttribute      = string(types.MessageSystemAttributeNameMessageGroupId)
)

// Message é uma mensagem recebida. ReceiveCount é o ApproximateReceiveCount do broker, que
// o consumidor usa para o backoff e para saber o quão perto a mensagem está da DLQ.
type Message struct {
	MessageID     string
	ReceiptHandle string
	Body          string
	GroupID       string
	ReceiveCount  int
	Attributes    map[string]string
}

// ReceiveParams controla uma recepção.
type ReceiveParams struct {
	QueueURL string
	// MaxMessages vai de 1 a 10.
	MaxMessages int
	// WaitSeconds é a espera longa, de 0 a 20.
	WaitSeconds int
	// VisibilityTimeoutSeconds, se maior que zero, substitui o da fila para esta recepção.
	VisibilityTimeoutSeconds int
}

// SendParams descreve o envio a uma fila FIFO.
type SendParams struct {
	QueueURL        string
	Body            string
	GroupID         string
	DeduplicationID string
	Attributes      map[string]string
}

// Client é o cliente SQS do serviço.
type Client struct {
	api     *sqs.Client
	timeout time.Duration
}

// New cria o cliente. Nenhuma chamada de rede é feita aqui.
func New(ctx context.Context, config Config) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}

	config = config.withDefaults()

	awsConfig, err := loadAWSConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("sqsclient: load aws config: %w", err)
	}

	api := sqs.NewFromConfig(awsConfig, func(options *sqs.Options) {
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
	})

	return &Client{api: api, timeout: config.RequestTimeout}, nil
}

func loadAWSConfig(ctx context.Context, config Config) (aws.Config, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(config.Region)}

	if config.AccessKeyID != "" {
		static := credentials.NewStaticCredentialsProvider(
			config.AccessKeyID,
			config.SecretAccessKey,
			"",
		)
		options = append(options, awsconfig.WithCredentialsProvider(static))
	}

	return awsconfig.LoadDefaultConfig(ctx, options...)
}

// Receive busca até MaxMessages mensagens, com espera longa opcional.
func (client *Client) Receive(ctx context.Context, params ReceiveParams) ([]Message, error) {
	wait := min(max(params.WaitSeconds, 0), maxLongPollSecond)

	callContext, cancel := context.WithTimeout(ctx, client.timeout+time.Duration(wait)*time.Second)
	defer cancel()

	output, err := client.api.ReceiveMessage(callContext, &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(params.QueueURL),
		MaxNumberOfMessages: int32(min(max(params.MaxMessages, 1), maxBatch)),
		WaitTimeSeconds:     int32(wait),
		VisibilityTimeout:   int32(max(params.VisibilityTimeoutSeconds, 0)),
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		return nil, translateError(err)
	}

	return messagesOf(output.Messages), nil
}

// Delete remove a mensagem da fila; deve ser chamado só depois do commit do tratamento.
func (client *Client) Delete(ctx context.Context, queueURL string, receiptHandle string) error {
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	_, err := client.api.DeleteMessage(callContext, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(queueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})

	return translateError(err)
}

// ChangeVisibility redefine por quantos segundos a mensagem fica invisível; zero a devolve
// à fila na hora.
func (client *Client) ChangeVisibility(
	ctx context.Context,
	queueURL string,
	receiptHandle string,
	seconds int,
) error {
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	_, err := client.api.ChangeMessageVisibility(callContext, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(queueURL),
		ReceiptHandle:     aws.String(receiptHandle),
		VisibilityTimeout: int32(max(seconds, 0)),
	})

	return translateError(err)
}

// Send envia uma mensagem a uma fila FIFO e devolve o ID atribuído pelo broker.
func (client *Client) Send(ctx context.Context, params SendParams) (string, error) {
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	output, err := client.api.SendMessage(callContext, &sqs.SendMessageInput{
		QueueUrl:               aws.String(params.QueueURL),
		MessageBody:            aws.String(params.Body),
		MessageGroupId:         aws.String(params.GroupID),
		MessageDeduplicationId: aws.String(params.DeduplicationID),
		MessageAttributes:      attributesOf(params.Attributes),
	})
	if err != nil {
		return "", translateError(err)
	}

	return aws.ToString(output.MessageId), nil
}

// QueueAttributes devolve todos os atributos da fila (ARN, política de redrive, etc.).
func (client *Client) QueueAttributes(
	ctx context.Context,
	queueURL string,
) (map[string]string, error) {
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	output, err := client.api.GetQueueAttributes(callContext, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameAll},
	})
	if err != nil {
		return nil, translateError(err)
	}

	return output.Attributes, nil
}

// CreateFIFOQueue cria uma fila FIFO sem deduplicação por conteúdo e devolve a sua URL.
// extra acrescenta atributos (VisibilityTimeout, RedrivePolicy). Usado pelos testes e por
// ferramentas de provisionamento; em ambiente real as filas vêm do script do projeto.
func (client *Client) CreateFIFOQueue(
	ctx context.Context,
	name string,
	extra map[string]string,
) (string, error) {
	callContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()

	attributes := map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"}
	for key, value := range extra {
		attributes[key] = value
	}

	output, err := client.api.CreateQueue(callContext, &sqs.CreateQueueInput{
		QueueName:  aws.String(name),
		Attributes: attributes,
	})
	if err != nil {
		return "", translateError(err)
	}

	return aws.ToString(output.QueueUrl), nil
}

func messagesOf(received []types.Message) []Message {
	messages := make([]Message, 0, len(received))

	for _, message := range received {
		count, _ := strconv.Atoi(message.Attributes[receiveCountAttribute])

		messages = append(messages, Message{
			MessageID:     aws.ToString(message.MessageId),
			ReceiptHandle: aws.ToString(message.ReceiptHandle),
			Body:          aws.ToString(message.Body),
			GroupID:       message.Attributes[groupIDAttribute],
			ReceiveCount:  count,
			Attributes:    attributesFrom(message.MessageAttributes),
		})
	}

	return messages
}

func attributesOf(attributes map[string]string) map[string]types.MessageAttributeValue {
	if len(attributes) == 0 {
		return nil
	}

	converted := make(map[string]types.MessageAttributeValue, len(attributes))
	for name, value := range attributes {
		converted[name] = types.MessageAttributeValue{
			DataType:    aws.String("String"),
			StringValue: aws.String(value),
		}
	}

	return converted
}

func attributesFrom(attributes map[string]types.MessageAttributeValue) map[string]string {
	converted := make(map[string]string, len(attributes))
	for name, value := range attributes {
		converted[name] = aws.ToString(value.StringValue)
	}

	return converted
}
