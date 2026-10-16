-- Preserva os metadados de rastreio da requisição original, para que eventos emitidos
-- depois (resolução de referência pendente) mantenham correlationId e causationId.
ALTER TABLE wager_transactions
    ADD COLUMN correlation_id text,
    ADD COLUMN causation_id text;
