package main

import (
	"net"
	"os"
	"time"
)

const (
	productionSubscriptionStateRoot = "/var/lib/liquid-formula/subscriptions"
	productionSubscriptionLockPath  = "/var/run/liquid-formula/subscription.lock"
)

const productionSubscriptionLockRetry = 50 * time.Millisecond

type subscriptionEngineRuntime struct {
	StateRoot string
	LockPath  string
	LockRetry time.Duration
}

func defaultSubscriptionEngineRuntime() subscriptionEngineRuntime {
	return subscriptionEngineRuntime{
		StateRoot: productionSubscriptionStateRoot,
		LockPath:  productionSubscriptionLockPath,
		LockRetry: productionSubscriptionLockRetry,
	}
}

func newProductionServeDependencies(
	runtime subscriptionEngineRuntime,
) serveDependencies {
	return serveDependencies{
		ReadFile: os.ReadFile,
		Listen:   net.Listen,
		NewEngine: func(config gatewayConfig) aggregateEngine {
			state, err := diskOpenOrCreateState(
				runtime.StateRoot,
				nativeDiskGenerationFilesystem{},
			)
			if err != nil {
				return nil
			}
			state.close()
			legacy := newFileLegacySourceProvider(
				runtime.StateRoot,
				config.LegacyNodePath,
			)
			engine := newSubscriptionAggregateEngine(
				config,
				subscriptionEngineDependencies{
					Locker: newFileSubscriptionLocker(
						runtime.LockPath,
						runtime.LockRetry,
					),
					Store: newDiskGenerationStoreWithDependencies(
						runtime.StateRoot,
						diskGenerationStoreDependencies{
							Legacy: legacy,
						},
					),
					Fetcher:    newHTTPSourceFetcher(nil),
					Normalizer: productionSourceNormalizer{},
					Legacy:     legacy,
				},
			)
			return &attemptRecordingEngine{
				aggregateEngine: engine,
				recorder: newDiskLastAttemptRecorder(
					runtime.StateRoot,
				),
			}
		},
	}
}

type productionSourceNormalizer struct{}

func (productionSourceNormalizer) Normalize(
	raw []byte,
) ([]byte, NormalizeInfo, error) {
	normalized, info, err := NormalizeDocument(raw)
	if err != nil {
		return nil, info, err
	}
	canonical, count, err := canonicalizeStoredSource(normalized)
	if err != nil ||
		count != info.Accepted ||
		len(canonical) == 0 {
		return nil, info, normalizeError(
			"output_encode_failed", info.Format,
		)
	}
	return canonical, info, nil
}
