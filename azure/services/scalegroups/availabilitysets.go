/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scalegroups

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-12-01/compute"
	"github.com/pkg/errors"
	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/async"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/resourceskus"
	"sigs.k8s.io/cluster-api-provider-azure/util/reconciler"
	"sigs.k8s.io/cluster-api-provider-azure/util/tele"
)

const serviceName = "scalegroups"

// ScaleGroupScope defines the scope interface for a availability sets service.
type ScaleGroupScope interface {
	azure.ClusterDescriber
	azure.AsyncStatusUpdater
	ScaleGroupSpec() azure.ResourceSpecGetter
}

// Service provides operations on Azure resources.
type Service struct {
	Scope ScaleGroupScope
	async.Getter
	async.Reconciler
	resourceSKUCache *resourceskus.Cache
}

// New creates a new availability sets service.
func New(scope ScaleGroupScope, skuCache *resourceskus.Cache) *Service {
	client := NewFlexClient(scope)
	return &Service{
		Scope:            scope,
		Getter:           client,
		resourceSKUCache: skuCache,
		Reconciler:       async.New(scope, client, client),
	}
}

// Name returns the service name.
func (s *Service) Name() string {
	return serviceName
}

// Reconcile creates or updates availability sets.
func (s *Service) Reconcile(ctx context.Context) error {
	ctx, log, done := tele.StartSpanWithLogger(ctx, "scalegroups.Service.Reconcile")
	defer done()

	ctx, cancel := context.WithTimeout(ctx, reconciler.DefaultAzureServiceReconcileTimeout)
	defer cancel()

	var err error
	if setSpec := s.Scope.ScaleGroupSpec(); setSpec != nil {
		_, err = s.CreateResource(ctx, setSpec, serviceName)
	} else {
		log.V(2).Info("skip creation when no availability set spec is found")
		return nil
	}

	s.Scope.UpdatePutStatus(infrav1.AvailabilitySetReadyCondition, serviceName, err)
	return err
}

// Delete deletes availability sets.
func (s *Service) Delete(ctx context.Context) (err error) {
	ctx, log, done := tele.StartSpanWithLogger(ctx, "scalegroups.Service.Delete")
	defer done()

	ctx, cancel := context.WithTimeout(ctx, reconciler.DefaultAzureServiceReconcileTimeout)
	defer cancel()

	setSpec := s.Scope.ScaleGroupSpec()
	if setSpec == nil {
		log.V(2).Info("skip deletion when no availability set spec is found")
		return nil
	}

	defer func() {
		s.Scope.UpdateDeleteStatus(infrav1.AvailabilitySetReadyCondition, serviceName, err)
	}()

	existing, err := s.Get(ctx, setSpec)
	if err != nil {
		if !azure.ResourceNotFound(err) {
			return errors.Wrapf(err, "failed to get availability set %s in resource group %s", setSpec.ResourceName(), setSpec.ResourceGroupName())
		}
	}

	switch set := existing.(type) {
	case compute.AvailabilitySet:
		// TODO(jpang): Move this logic inside of DeleteAsync
		if set.VirtualMachines != nil && len(*set.VirtualMachines) > 0 {
			log.V(2).Info("skip deleting ScaleGroup set with VMs", "ScaleGroup", setSpec.ResourceName())
			return nil
		}
	case compute.VirtualMachineScaleSet:
		log.V(2).Info("case compute.VirtualMachineScaleSet", "set", set)
	default:
		return errors.Errorf("unexpected ScaleGroup type: %T", set)
	}

	if err = s.DeleteResource(ctx, setSpec, serviceName); err != nil {
		return errors.Wrapf(err, "failed to delete ScaleGroup %s in resource group %s", setSpec.ResourceName(), setSpec.ResourceGroupName())
	}
	return nil
}

// IsManaged returns always returns true as CAPZ does not support BYO availability set.
func (s *Service) IsManaged(ctx context.Context) (bool, error) {
	return true, nil
}
