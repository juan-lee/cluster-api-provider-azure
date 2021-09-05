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

package scalesets

import (
	"context"
	"strconv"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-04-01/compute"
	"github.com/pkg/errors"

	infrav1 "sigs.k8s.io/cluster-api-provider-azure/api/v1beta1"
	"sigs.k8s.io/cluster-api-provider-azure/azure"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/async"
	"sigs.k8s.io/cluster-api-provider-azure/azure/services/resourceskus"
	"sigs.k8s.io/cluster-api-provider-azure/util/reconciler"
	"sigs.k8s.io/cluster-api-provider-azure/util/tele"
)

// FlexibleScaleSetScope defines the scope interface for a scale sets service.
type FlexibleScaleSetScope interface {
	azure.ClusterDescriber
	azure.AsyncStatusUpdater
	FlexibleScaleSetSpec() azure.ResourceSpecGetter
}

// FlexService provides operations on Azure resources.
type FlexService struct {
	Scope FlexibleScaleSetScope
	async.Getter
	async.Reconciler
	resourceSKUCache *resourceskus.Cache
}

// NewFlexible creates a new service.
func NewFlexible(scope FlexibleScaleSetScope, skuCache *resourceskus.Cache) *FlexService {
	client := NewClient(scope)
	return &FlexService{
		Scope:            scope,
		Getter:           client,
		resourceSKUCache: skuCache,
		Reconciler:       async.New(scope, client, client),
	}
}

// Reconcile creates or updates flexible scale sets.
func (s *FlexService) Reconcile(ctx context.Context) error {
	ctx, log, done := tele.StartSpanWithLogger(ctx, "scalesets.FlexService.Reconcile")
	defer done()

	ctx, cancel := context.WithTimeout(ctx, reconciler.DefaultAzureServiceReconcileTimeout)
	defer cancel()

	var err error
	if setSpec := s.Scope.FlexibleScaleSetSpec(); setSpec != nil {
		_, err = s.CreateResource(ctx, setSpec, serviceName)
	} else {
		log.V(2).Info("skip creation when no availability set spec is found")
		return nil
	}

	s.Scope.UpdatePutStatus(infrav1.AvailabilitySetReadyCondition, serviceName, err)
	return err
}

// Delete deletes scale sets.
func (s *FlexService) Delete(ctx context.Context) error {
	ctx, log, done := tele.StartSpanWithLogger(ctx, "scalesets.Service.Delete")
	defer done()

	ctx, cancel := context.WithTimeout(ctx, reconciler.DefaultAzureServiceReconcileTimeout)
	defer cancel()

	var resultingErr error
	setSpec := s.Scope.FlexibleScaleSetSpec()
	if setSpec == nil {
		log.V(2).Info("skip deletion when no scale set spec is found")
		return nil
	}

	existingSet, err := s.Get(ctx, setSpec)
	if err != nil {
		if !azure.ResourceNotFound(err) {
			resultingErr = errors.Wrapf(err, "failed to get scale set %s in resource group %s", setSpec.ResourceName(), setSpec.ResourceGroupName())
		}
	} else {
		vmss, ok := existingSet.(compute.VirtualMachineScaleSet)
		if !ok {
			resultingErr = errors.Errorf("%T is not a compute.VirtualMachineScaleSet", existingSet)
		} else {
			// only delete when the scale set does not have any vms
			if vmss.VirtualMachineScaleSetProperties != nil {
				log.V(2).Info("skip deleting scale set with VMs", "vmss", setSpec.ResourceName())
			} else {
				resultingErr = s.DeleteResource(ctx, setSpec, serviceName)
			}
		}
	}

	s.Scope.UpdateDeleteStatus(infrav1.AvailabilitySetReadyCondition, serviceName, resultingErr)
	return resultingErr
}

func (s *FlexService) faultDomainCount(ctx context.Context) (int32, error) {
	asSku, err := s.resourceSKUCache.Get(ctx, string(compute.AvailabilitySetSkuTypesAligned), resourceskus.AvailabilitySets)
	if err != nil {
		return -1, errors.Wrap(err, "failed to get availability sets sku")
	}

	faultDomainCountStr, ok := asSku.GetCapability(resourceskus.MaximumPlatformFaultDomainCount)
	if !ok {
		return -1, errors.Errorf("cannot find capability %s sku %s", resourceskus.MaximumPlatformFaultDomainCount, *asSku.Name)
	}

	faultDomainCount, err := strconv.ParseUint(faultDomainCountStr, 10, 32)
	if err != nil {
		return -1, errors.Wrap(err, "failed to determine max fault domain count")
	}
	return int32(faultDomainCount), nil
}
