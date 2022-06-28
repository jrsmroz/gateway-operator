package controllers

import (
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	operatorv1alpha1 "github.com/kong/gateway-operator/api/v1alpha1"
	gatewayutils "github.com/kong/gateway-operator/internal/utils/gateway"
	k8sutils "github.com/kong/gateway-operator/internal/utils/kubernetes"
	operatorerrors "github.com/kong/gateway-operator/pkg/errors"
	"github.com/kong/gateway-operator/pkg/vars"
)

// -----------------------------------------------------------------------------
// GatewayReconciler - Reconciler Helpers
// -----------------------------------------------------------------------------

func (r *GatewayReconciler) createDataPlane(ctx context.Context, gateway *gatewayv1alpha2.Gateway, gatewayConfig *operatorv1alpha1.GatewayConfiguration) error {
	dataplane := &operatorv1alpha1.DataPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gateway.Namespace,
			Name:      "dataplane-" + gateway.Name,
		},
	}
	if gatewayConfig.Spec.DataPlaneDeploymentOptions != nil {
		dataplane.Spec.DataPlaneDeploymentOptions = *gatewayConfig.Spec.DataPlaneDeploymentOptions
	}
	k8sutils.SetOwnerForObject(dataplane, gateway)
	gatewayutils.LabelObjectAsGatewayManaged(dataplane)
	return r.Client.Create(ctx, dataplane)
}

func (r *GatewayReconciler) createControlPlane(
	ctx context.Context,
	gatewayClass *gatewayv1alpha2.GatewayClass,
	gateway *gatewayv1alpha2.Gateway,
	gatewayConfig *operatorv1alpha1.GatewayConfiguration,
	dataplaneName string,
) error {
	controlplane := &operatorv1alpha1.ControlPlane{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gateway.Namespace,
			Name:      "controlplane-" + gateway.Name, // TODO: generated names https://github.com/Kong/gateway-operator/issues/21
		},
		Spec: operatorv1alpha1.ControlPlaneSpec{
			ControlPlaneDeploymentOptions: operatorv1alpha1.ControlPlaneDeploymentOptions{
				DataPlane: &dataplaneName,
			},
			GatewayClass: (*gatewayv1alpha2.ObjectName)(&gatewayClass.Name),
		},
	}
	if gatewayConfig.Spec.ControlPlaneDeploymentOptions != nil {
		controlplane.Spec.ControlPlaneDeploymentOptions = *gatewayConfig.Spec.ControlPlaneDeploymentOptions
	}
	k8sutils.SetOwnerForObject(controlplane, gateway)
	gatewayutils.LabelObjectAsGatewayManaged(controlplane)
	return r.Client.Create(ctx, controlplane)
}

func (r *GatewayReconciler) ensureGatewayMarkedReady(ctx context.Context, gateway *gatewayv1alpha2.Gateway) error {
	if !gatewayutils.IsGatewayReady(gateway) {
		svc := new(corev1.Service)
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: gateway.Namespace, Name: fmt.Sprintf("svc-dataplane-%s", gateway.Name)}, svc); err != nil {
			return err
		}

		if svc.Spec.ClusterIP == "" {
			return fmt.Errorf("service %s doesn't have a ClusterIP yet, not ready", svc.Name)
		}

		gatewayIPs := make([]string, 0)
		if len(svc.Status.LoadBalancer.Ingress) > 0 {
			gatewayIPs = append(gatewayIPs, svc.Status.LoadBalancer.Ingress[0].IP) // TODO: handle hostnames https://github.com/Kong/gateway-operator/issues/24
		}

		newAddresses := make([]gatewayv1alpha2.GatewayAddress, 0, len(gatewayIPs))
		ipaddrT := gatewayv1alpha2.IPAddressType
		for _, ip := range append(gatewayIPs, svc.Spec.ClusterIP) {
			newAddresses = append(newAddresses, gatewayv1alpha2.GatewayAddress{
				Type:  &ipaddrT,
				Value: ip,
			})
		}

		gateway.Status.Addresses = newAddresses

		gateway = gatewayutils.PruneGatewayStatusConds(gateway)
		newConditions := make([]metav1.Condition, 0, len(gateway.Status.Conditions))
		newConditions = append(newConditions, metav1.Condition{
			Type:               string(gatewayv1alpha2.GatewayConditionReady),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gateway.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatewayv1alpha2.GatewayReasonReady),
		})
		gateway.Status.Conditions = newConditions
		return r.Client.Status().Update(ctx, gateway)
	}

	return nil
}

func (r *GatewayReconciler) verifyGatewayClassSupport(ctx context.Context, gateway *gatewayv1alpha2.Gateway) (*gatewayv1alpha2.GatewayClass, error) {
	if gateway.Spec.GatewayClassName == "" {
		return nil, operatorerrors.ErrUnsupportedGateway
	}

	gwc := new(gatewayv1alpha2.GatewayClass)
	if err := r.Client.Get(ctx, client.ObjectKey{Name: string(gateway.Spec.GatewayClassName)}, gwc); err != nil {
		return nil, err
	}

	if string(gwc.Spec.ControllerName) != vars.ControllerName {
		return nil, operatorerrors.ErrUnsupportedGateway
	}

	return gwc, nil
}

func (r *GatewayReconciler) getOrCreateGatewayConfiguration(ctx context.Context, gatewayClass *gatewayv1alpha2.GatewayClass) (*operatorv1alpha1.GatewayConfiguration, error) {
	if gatewayClass.Spec.ParametersRef == nil {
		return new(operatorv1alpha1.GatewayConfiguration), nil
	}
	return r.getGatewayConfigForGatewayClass(ctx, gatewayClass)
}

func (r *GatewayReconciler) getGatewayConfigForGatewayClass(ctx context.Context, gatewayClass *gatewayv1alpha2.GatewayClass) (*operatorv1alpha1.GatewayConfiguration, error) {
	if string(gatewayClass.Spec.ParametersRef.Group) != operatorv1alpha1.GroupVersion.Group ||
		string(gatewayClass.Spec.ParametersRef.Kind) != "GatewayConfiguration" {
		return nil, &k8serrors.StatusError{
			ErrStatus: metav1.Status{
				Status: metav1.StatusFailure,
				Code:   http.StatusBadRequest,
				Reason: metav1.StatusReasonInvalid,
				Details: &metav1.StatusDetails{
					Kind: string(gatewayClass.Spec.ParametersRef.Kind),
					Causes: []metav1.StatusCause{{
						Type: metav1.CauseTypeFieldValueNotSupported,
						Message: fmt.Sprintf("controller only supports %s %s resources for GatewayClass parametersRef",
							operatorv1alpha1.GroupVersion.Group, "GatewayConfiguration"),
					}},
				},
			}}
	}

	gatewayConfig := new(operatorv1alpha1.GatewayConfiguration)
	return gatewayConfig, r.Client.Get(ctx, client.ObjectKey{
		Namespace: string(*gatewayClass.Spec.ParametersRef.Namespace),
		Name:      gatewayClass.Spec.ParametersRef.Name,
	}, gatewayConfig)
}