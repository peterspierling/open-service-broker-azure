package redis

import (
	"context"
	"fmt"

	"github.com/Azure/open-service-broker-azure/pkg/async"
	log "github.com/Sirupsen/logrus"
	"github.com/go-redis/redis"
)

// engine is a Redis-based implementation of the Engine interface.
type engine struct {
	redisClient *redis.Client
	// This allows tests to inject an alternative implementation of Worker
	worker Worker
	// This allows tests to inject an alternative implementation of Cleaner
	cleaner Cleaner
}

// NewEngine returns a new Redis-based implementation of the aync.Engine
// interface
func NewEngine(redisClient *redis.Client) async.Engine {
	return &engine{
		redisClient: redisClient,
		cleaner:     newCleaner(redisClient),
		worker:      newWorker(redisClient),
	}
}

// RegisterJob registers a new async.JobFn with the async engine
func (e *engine) RegisterJob(name string, fn async.JobFn) error {
	return e.worker.RegisterJob(name, fn)
}

// SubmitTask submits an idempotent task to the async engine for reliable,
// asynchronous completion
func (e *engine) SubmitTask(task async.Task) error {
	taskJSON, err := task.ToJSON()
	if err != nil {
		return fmt.Errorf("error encoding task %#v: %s", task, err)
	}

	var queueName string
	if task.GetExecuteTime() != nil {
		queueName = deferredTaskQueueName
	} else {
		queueName = pendingTaskQueueName
	}

	err = e.redisClient.LPush(queueName, taskJSON).Err()
	if err != nil {
		return fmt.Errorf("error encoding task %#v: %s", task, err)
	}
	return nil
}

// Run causes the async engine to carry out all of its functions. It blocks
// until a fatal error is encountered or the context passed to it has been
// canceled. Run always returns a non-nil error.
func (e *engine) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error)
	// Start the cleaner
	go func() {
		select {
		case errCh <- &errCleanerStopped{err: e.cleaner.Run(ctx)}:
		case <-ctx.Done():
		}
	}()
	// Start the worker
	go func() {
		select {
		case errCh <- &errWorkerStopped{
			workerID: e.worker.GetID(),
			err:      e.worker.Run(ctx),
		}:
		case <-ctx.Done():
		}
	}()
	select {
	case <-ctx.Done():
		log.Debug("context canceled; async engine shutting down")
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}
