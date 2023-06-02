// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package kafka

import (
	"context"
	"errors"
	"fmt"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	apmqueue "github.com/elastic/apm-queue"
)

// ManagerConfig holds configuration for managing Kafka topics.
type ManagerConfig struct {
	CommonConfig
}

// finalize ensures the configuration is valid, setting default values from
// environment variables as described in doc comments, returning an error if
// any configuration is invalid.
func (cfg *ManagerConfig) finalize() error {
	var errs []error
	if err := cfg.CommonConfig.finalize(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// Manager manages Kafka topics.
type Manager struct {
	cfg    ManagerConfig
	client *kadm.Client
	tracer trace.Tracer
}

// NewManager returns a new Manager with the given config.
func NewManager(cfg ManagerConfig) (*Manager, error) {
	if err := cfg.finalize(); err != nil {
		return nil, fmt.Errorf("kafka: invalid manager config: %w", err)
	}
	client, err := cfg.newClient()
	if err != nil {
		return nil, fmt.Errorf("kafka: failed creating kafka client: %w", err)
	}
	return &Manager{
		cfg:    cfg,
		client: kadm.NewClient(client),
		tracer: cfg.tracerProvider().Tracer("kafka"),
	}, nil
}

// Close closes the manager's resources, including its connections to the
// Kafka brokers and any associated goroutines.
func (m *Manager) Close() error {
	m.client.Close()
	return nil
}

// DeleteTopics deletes one or more topics.
//
// No error is returned for topics that do not exist.
func (m *Manager) DeleteTopics(ctx context.Context, topics ...apmqueue.Topic) error {
	// TODO(axw) how should we record topics?
	ctx, span := m.tracer.Start(ctx, "DeleteTopics", trace.WithAttributes(
		semconv.MessagingSystemKey.String("kafka"),
	))
	defer span.End()

	topicNames := make([]string, len(topics))
	for i, topic := range topics {
		topicNames[i] = string(topic)
	}
	responses, err := m.client.DeleteTopics(ctx, topicNames...)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "DeleteTopics returned an error")
		return fmt.Errorf("failed to delete kafka topics: %w", err)
	}
	var deleteErrors []error
	for _, response := range responses.Sorted() {
		logger := m.cfg.Logger.With(zap.String("topic", response.Topic))
		if err := response.Err; err != nil {
			if errors.Is(err, kerr.UnknownTopicOrPartition) {
				logger.Debug("kafka topic does not exist")
			} else {
				span.RecordError(err)
				span.SetStatus(codes.Error, "failed to delete one or more topic")
				deleteErrors = append(deleteErrors,
					fmt.Errorf(
						"failed to delete topic %q: %w",
						response.Topic, err,
					),
				)
			}
			continue
		}
		logger.Info("deleted kafka topic")
	}
	return errors.Join(deleteErrors...)

}