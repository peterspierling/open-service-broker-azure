package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/open-service-broker-azure/pkg/async"
	"github.com/Azure/open-service-broker-azure/pkg/service"
	log "github.com/Sirupsen/logrus"
)

func (b *broker) doUpdateStep(
	ctx context.Context,
	args map[string]string,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stepName, ok := args["stepName"]
	if !ok {
		return errors.New(`missing required argument "stepName"`)
	}
	instanceID, ok := args["instanceID"]
	if !ok {
		return errors.New(`missing required argument "instanceID"`)
	}
	instance, ok, err := b.store.GetInstance(instanceID)
	if err != nil {
		return b.handleUpdatingError(
			instanceID,
			stepName,
			err,
			"error loading persisted instance",
		)
	}
	if !ok {
		return b.handleUpdatingError(
			instanceID,
			stepName,
			nil,
			"instance does not exist in the data store",
		)
	}
	log.WithFields(log.Fields{
		"step":       stepName,
		"instanceID": instance.InstanceID,
	}).Debug("executing updating step")
	svc, ok := b.catalog.GetService(instance.ServiceID)
	if !ok {
		return b.handleUpdatingError(
			instance,
			stepName,
			nil,
			fmt.Sprintf(
				`no service was found for handling serviceID "%s"`,
				instance.ServiceID,
			),
		)
	}
	plan, ok := svc.GetPlan(instance.PlanID)
	if !ok {
		return b.handleUpdatingError(
			instance,
			stepName,
			nil,
			fmt.Sprintf(
				`no plan was found for handling planID "%s"`,
				instance.ServiceID,
			),
		)
	}
	serviceManager := svc.GetServiceManager()

	// Retrieve a second copy of the instance from storage. Why? We're about to
	// pass the instance off to module specific code. It's passed by value, so
	// we'd like to imagine that the modules can only modify copies of the
	// instance, but some of the fields of an instance are pointers and a copy of
	// a pointer still points back to the original thing-- meaning modules could
	// modify parts of the instance in unexpected ways. What we'll do is take the
	// one part of the instance that we intend for modules to modify (instance
	// details) and add that to this untouched copy and write the untouched copy
	// back to storage.
	instanceCopy, _, err := b.store.GetInstance(instanceID)
	if err != nil {
		return b.handleProvisioningError(
			instanceID,
			stepName,
			err,
			"error loading persisted instance",
		)
	}

	updater, err := serviceManager.GetUpdater(plan)
	if err != nil {
		return b.handleUpdatingError(
			instance,
			stepName,
			err,
			fmt.Sprintf(
				`error retrieving updater for service "%s"`,
				instance.ServiceID,
			),
		)
	}
	step, ok := updater.GetStep(stepName)
	if !ok {
		return b.handleUpdatingError(
			instance,
			stepName,
			nil,
			`updater does not know how to process step "%s"`,
		)
	}
	updatedDetails, err := step.Execute(
		ctx,
		instance,
		plan,
	)
	if err != nil {
		return b.handleUpdatingError(
			instance,
			stepName,
			err,
			"error executing updating step",
		)
	}
	instanceCopy.Details = updatedDetails
	if nextStepName, ok := updater.GetNextStepName(step.GetName()); ok {
		if err = b.store.WriteInstance(instanceCopy); err != nil {
			return b.handleUpdatingError(
				instanceCopy,
				stepName,
				err,
				"error persisting instance",
			)
		}
		task := async.NewTask(
			"updateStep",
			map[string]string{
				"stepName":   nextStepName,
				"instanceID": instanceID,
			},
		)
		if err = b.asyncEngine.SubmitTask(task); err != nil {
			return b.handleUpdatingError(
				instanceCopy,
				stepName,
				err,
				fmt.Sprintf(`error enqueing next step: "%s"`, nextStepName),
			)
		}
	} else {
		// No next step-- we're done updating!
		instanceCopy.Status = service.InstanceStateUpdated
		if err = b.store.WriteInstance(instanceCopy); err != nil {
			return b.handleUpdatingError(
				instanceCopy,
				stepName,
				err,
				"error persisting instance",
			)
		}
	}
	return nil
}

// handleUpdatingError tries to handle async updating errors. If an
// instance is passed in, its status is updated and an attempt is made to
// persist the instance with updated status. If this fails, we have a very
// serious problem on our hands, so we log that failure and kill the process.
// Barring such a failure, a nicely formatted error is returned to be, in-turn,
// returned by the caller of this function. If an instanceID is passed in
// (instead of an instance), only error formatting is handled.
func (b *broker) handleUpdatingError(
	instanceOrInstanceID interface{},
	stepName string,
	e error,
	msg string,
) error {
	instance, ok := instanceOrInstanceID.(service.Instance)
	if !ok {
		instanceID := instanceOrInstanceID
		if e == nil {
			return fmt.Errorf(
				`error executing updating step "%s" for instance "%s": %s`,
				stepName,
				instanceID,
				msg,
			)
		}
		return fmt.Errorf(
			`error executing updating step "%s" for instance "%s": %s: %s`,
			stepName,
			instanceID,
			msg,
			e,
		)
	}
	// If we get to here, we have an instance (not just an instanceID)
	instance.Status = service.InstanceStateUpdatingFailed
	var ret error
	if e == nil {
		ret = fmt.Errorf(
			`error executing updating step "%s" for instance "%s": %s`,
			stepName,
			instance.InstanceID,
			msg,
		)
	} else {
		ret = fmt.Errorf(
			`error executing updating step "%s" for instance "%s": %s: %s`,
			stepName,
			instance.InstanceID,
			msg,
			e,
		)
	}
	instance.StatusReason = ret.Error()
	if err := b.store.WriteInstance(instance); err != nil {
		log.WithFields(log.Fields{
			"instanceID":       instance.InstanceID,
			"status":           instance.Status,
			"originalError":    ret,
			"persistenceError": err,
		}).Fatal("error persisting instance with updated status")
	}
	return ret
}
