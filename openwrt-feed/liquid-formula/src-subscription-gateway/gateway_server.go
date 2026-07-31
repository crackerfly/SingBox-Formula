package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

const gatewayServiceName = "liquid-formula-subscription-gateway"

type aggregateFailureCode string

const (
	aggregateCodeNoSources         aggregateFailureCode = "no_sources"
	aggregateCodeBusy              aggregateFailureCode = "busy"
	aggregateCodeSourceUnavailable aggregateFailureCode = "source_unavailable"
	aggregateCodeStateInvalid      aggregateFailureCode = "state_invalid"
	aggregateCodeAggregateInvalid  aggregateFailureCode = "aggregate_invalid"
	aggregateCodeCommitFailed      aggregateFailureCode = "commit_failed"
)

type aggregateOutcome struct {
	Bytes            []byte
	Code             aggregateFailureCode
	SourceIndex      int
	Preserved        bool
	FailureStage     failureStage
	FetchCode        sourceFetchCode
	ActiveGeneration string
	CurrentObserved  bool
}

type aggregateEngine interface {
	Aggregate(context.Context) aggregateOutcome
}

type unavailableAggregateEngine struct {
	sourceCount int
}

func (engine unavailableAggregateEngine) Aggregate(context.Context) aggregateOutcome {
	if engine.sourceCount == 0 {
		return aggregateOutcome{Code: aggregateCodeNoSources}
	}
	return aggregateOutcome{
		Code:        aggregateCodeSourceUnavailable,
		SourceIndex: 1,
	}
}

type gatewayHTTPHandler struct {
	config gatewayConfig
	engine aggregateEngine
}

type aggregateErrorBody struct {
	Service      string               `json:"service"`
	Status       string               `json:"status"`
	Code         aggregateFailureCode `json:"code"`
	FailureStage failureStage         `json:"failure_stage,omitempty"`
	FetchCode    sourceFetchCode      `json:"fetch_code,omitempty"`
	SourceIndex  int                  `json:"source_index"`
	Preserved    bool                 `json:"preserved"`
}

func openGatewayServer(
	config gatewayConfig,
	engine aggregateEngine,
	listen func(string, string) (net.Listener, error),
) (*http.Server, net.Listener, error) {
	if config.ListenAddress != "127.0.0.1" ||
		config.ListenPort < 1 || config.ListenPort > 65535 ||
		config.AggregateTimeoutSeconds < 1 ||
		config.WriteTimeoutSeconds < 1 ||
		engine == nil || listen == nil {
		return nil, nil, fmt.Errorf("gateway server configuration invalid")
	}
	address := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", config.ListenPort))
	listener, err := listen("tcp4", address)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway listener unavailable")
	}
	server := &http.Server{
		Handler: gatewayHTTPHandler{
			config: config,
			engine: engine,
		},
		WriteTimeout: time.Duration(config.WriteTimeoutSeconds) * time.Second,
	}
	return server, listener, nil
}

func (handler gatewayHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/health":
		if request.Method != http.MethodGet {
			writeMethodNotAllowed(writer)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(
			`{"service":"` + gatewayServiceName +
				`","status":"ok","config_digest":"` +
				handler.config.ConfigDigest + `"}`,
		))
	case "/v1/aggregate":
		if request.Method != http.MethodGet {
			writeMethodNotAllowed(writer)
			return
		}
		handler.writeAggregate(writer, request)
	default:
		writer.WriteHeader(http.StatusNotFound)
	}
}

func (handler gatewayHTTPHandler) writeAggregate(
	writer http.ResponseWriter,
	request *http.Request,
) {
	ctx, cancel := context.WithTimeout(
		request.Context(),
		time.Duration(handler.config.AggregateTimeoutSeconds)*time.Second,
	)
	defer cancel()
	outcome := aggregateWithinDeadline(ctx, handler.engine)

	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	if outcome.Code == "" &&
		len(outcome.Bytes) != 0 &&
		outcome.SourceIndex == 0 &&
		!outcome.Preserved {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(outcome.Bytes)
		return
	}
	if outcome.Code == "" {
		outcome = aggregateOutcome{Code: aggregateCodeStateInvalid}
	}

	status, safeOutcome := safeAggregateFailure(
		outcome,
		len(handler.config.Sources),
	)
	if safeOutcome.Code != aggregateCodeBusy {
		if recorder, ok := handler.engine.(interface {
			RecordFailure(gatewayConfig, aggregateOutcome) error
		}); ok {
			_ = recorder.RecordFailure(handler.config, safeOutcome)
		}
	}
	body, err := json.Marshal(aggregateErrorBody{
		Service:      gatewayServiceName,
		Status:       "error",
		Code:         safeOutcome.Code,
		FailureStage: safeOutcome.FailureStage,
		FetchCode:    safeOutcome.FetchCode,
		SourceIndex:  safeOutcome.SourceIndex,
		Preserved:    safeOutcome.Preserved,
	})
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(
			`{"service":"` + gatewayServiceName +
				`","status":"error","code":"state_invalid","source_index":0,"preserved":false}`,
		)
	}
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func aggregateWithinDeadline(
	ctx context.Context,
	engine aggregateEngine,
) aggregateOutcome {
	ctx, gate := withNewCurrentCommitGate(ctx)
	result := make(chan aggregateOutcome, 1)
	go func() {
		outcome := aggregateOutcome{Code: aggregateCodeStateInvalid}
		defer func() {
			if recover() != nil {
				outcome = aggregateOutcome{Code: aggregateCodeStateInvalid}
			}
			result <- outcome
		}()
		outcome = engine.Aggregate(ctx)
	}()

	select {
	case outcome := <-result:
		if ctx.Err() != nil {
			gate.deny()
			if !gate.begun() {
				return aggregateOutcome{
					Code:         aggregateCodeStateInvalid,
					FailureStage: failureStageDeadline,
				}
			}
		}
		return outcome
	case <-ctx.Done():
		gate.deny()
		if !gate.begun() {
			return aggregateOutcome{
				Code:         aggregateCodeStateInvalid,
				FailureStage: failureStageDeadline,
			}
		}
		return <-result
	}
}

func safeAggregateFailure(
	outcome aggregateOutcome,
	sourceCount int,
) (int, aggregateOutcome) {
	if !safeAggregateFailureDiagnostics(outcome) {
		return failedClosedAggregateOutcome()
	}
	switch outcome.Code {
	case aggregateCodeNoSources, aggregateCodeBusy:
		if outcome.SourceIndex != 0 {
			return failedClosedAggregateOutcome()
		}
		return http.StatusServiceUnavailable, outcome
	case aggregateCodeSourceUnavailable:
		if outcome.SourceIndex < 1 || outcome.SourceIndex > sourceCount {
			return failedClosedAggregateOutcome()
		}
		return http.StatusBadGateway, outcome
	case aggregateCodeStateInvalid,
		aggregateCodeAggregateInvalid,
		aggregateCodeCommitFailed:
		if outcome.SourceIndex != 0 {
			return failedClosedAggregateOutcome()
		}
		return http.StatusInternalServerError, outcome
	default:
		return failedClosedAggregateOutcome()
	}
}

func safeAggregateFailureDiagnostics(outcome aggregateOutcome) bool {
	if outcome.FailureStage == "" && outcome.FetchCode == "" {
		return true
	}
	if !validFailureStage(outcome.FailureStage) {
		return false
	}
	switch outcome.Code {
	case aggregateCodeNoSources:
		return outcome.FailureStage == failureStageConfiguration &&
			outcome.FetchCode == ""
	case aggregateCodeBusy:
		return false
	case aggregateCodeSourceUnavailable:
		if !validFetchFailureCode(outcome.FetchCode) {
			return false
		}
		if outcome.FetchCode == fetchCodeNormalize {
			return outcome.FailureStage == failureStageSourceNormalize
		}
		return outcome.FailureStage == failureStageSourceFetch
	case aggregateCodeStateInvalid:
		if outcome.FetchCode != "" {
			return false
		}
		switch outcome.FailureStage {
		case failureStageConfiguration,
			failureStageCurrentState,
			failureStageCommit,
			failureStageDeadline:
			return true
		default:
			return false
		}
	case aggregateCodeAggregateInvalid:
		return outcome.FailureStage == failureStageAggregate &&
			outcome.FetchCode == ""
	case aggregateCodeCommitFailed:
		return outcome.FailureStage == failureStageCommit &&
			outcome.FetchCode == ""
	default:
		return false
	}
}

func failedClosedAggregateOutcome() (int, aggregateOutcome) {
	return http.StatusInternalServerError, aggregateOutcome{
		Code: aggregateCodeStateInvalid,
	}
}

func writeMethodNotAllowed(writer http.ResponseWriter) {
	writer.Header().Set("Allow", http.MethodGet)
	writer.WriteHeader(http.StatusMethodNotAllowed)
}
