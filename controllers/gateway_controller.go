package controllers

import (
	"context"
	"errors"
	"fmt"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	operatorv1alpha1 "github.com/kong/gateway-operator/api/v1alpha1"
	gatewayutils "github.com/kong/gateway-operator/internal/utils/gateway"
	operatorerrors "github.com/kong/gateway-operator/pkg/errors"
	"github.com/kong/gateway-operator/pkg/vars"
)

// -----------------------------------------------------------------------------
// GatewayReconciler
// -----------------------------------------------------------------------------

// GatewayReconciler reconciles a Gateway object
type GatewayReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// SetupWithManager sets up the controller with the Manager.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	c, err := controller.New("gateway", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// watch Gateway objects, filtering out any Gateways which are not configured with
	// a supported GatewayClass controller name.
	if err := c.Watch(
		&source.Kind{Type: &gatewayv1alpha2.Gateway{}},
		&handler.EnqueueRequestForObject{},
		predicate.NewPredicateFuncs(r.gatewayHasMatchingGatewayClass),
	); err != nil {
		return err
	}

	// watch for updates to GatewayClasses, if any GatewayClasses change, enqueue
	// reconciliation for all supported gateway objects which reference it.
	if err := c.Watch(
		&source.Kind{Type: &gatewayv1alpha2.GatewayClass{}},
		handler.EnqueueRequestsFromMapFunc(r.listGatewaysForGatewayClass),
		predicate.NewPredicateFuncs(r.gatewayClassMatchesController),
	); err != nil {
		return err
	}

	// watch for updates to GatewayConfigurations, if any configuration targets a
	// Gateway that is supported, enqueue that Gateway.
	return c.Watch(
		&source.Kind{Type: &operatorv1alpha1.GatewayConfiguration{}},
		handler.EnqueueRequestsFromMapFunc(r.listGatewaysForGatewayConfig),
		predicate.NewPredicateFuncs(r.gatewayConfigurationMatchesController),
	)
}

// Reconcile moves the current state of an object to the intended state.
func (r *GatewayReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx).WithName("gateway")

	debug(log, "reconciling gateway resource", req)
	gateway := new(gatewayv1alpha2.Gateway)
	if err := r.Client.Get(ctx, req.NamespacedName, gateway); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	debug(log, "checking gatewayclass", gateway)
	gatewayClass, err := r.verifyGatewayClassSupport(ctx, gateway)
	if err != nil {
		if errors.Is(err, operatorerrors.ErrUnsupportedGateway) {
			debug(log, "resource not supported, ignoring", gateway, "ExpectedGatewayClass", vars.ControllerName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	debug(log, "resource is supported, ensuring that it gets marked as scheduled", gateway)
	if !gatewayutils.IsGatewayScheduled(gateway) {
		gateway.Status.Conditions = append(gateway.Status.Conditions, metav1.Condition{
			Type:               string(gatewayv1alpha2.GatewayConditionScheduled),
			Status:             metav1.ConditionTrue,
			ObservedGeneration: gateway.Generation,
			LastTransitionTime: metav1.Now(),
			Reason:             string(gatewayv1alpha2.GatewayReasonScheduled),
			Message:            fmt.Sprintf("this gateway has been picked up by the %s and will be processed", vars.ControllerName),
		})
		return ctrl.Result{}, r.Status().Update(ctx, gatewayutils.PruneGatewayStatusConds(gateway))
	}

	debug(log, "determining configuration", gateway)
	gatewayConfig, err := r.getOrCreateGatewayConfiguration(ctx, gatewayClass)
	if err != nil {
		return ctrl.Result{}, err
	}
	r.setGatewayConfigDefaults(gateway, gatewayConfig)

	debug(log, "looking for associated dataplanes", gateway)
	dataplane := new(operatorv1alpha1.DataPlane)
	dataplaneNSN := types.NamespacedName{Namespace: req.Namespace, Name: "dataplane-" + req.Name} // TODO: generated names https://github.com/Kong/gateway-operator/issues/21
	if err := r.Client.Get(ctx, dataplaneNSN, dataplane); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, r.createDataPlane(ctx, gateway, gatewayConfig)
		}
		return ctrl.Result{}, err
	}

	debug(log, "ensuring dataplane config is up to date", gateway)
	if gatewayConfig.Spec.DataPlaneDeploymentOptions != nil {
		if !dataplaneSpecDeepEqual(&dataplane.Spec.DataPlaneDeploymentOptions, gatewayConfig.Spec.DataPlaneDeploymentOptions) {
			debug(log, "dataplane config is out of date, updating", gateway)
			dataplane.Spec.DataPlaneDeploymentOptions = *gatewayConfig.Spec.DataPlaneDeploymentOptions
			return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, r.Client.Update(ctx, dataplane)
		}
	}

	debug(log, "waiting for dataplane readiness", gateway)
	dataplaneReady := false
	for _, condition := range dataplane.Status.Conditions {
		if condition.Type == string(DataPlaneConditionTypeProvisioned) && condition.Status == metav1.ConditionTrue {
			dataplaneReady = true
		}
	}
	if !dataplaneReady {
		debug(log, "dataplane not ready yet, waiting", gateway)
		return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, nil
	}

	debug(log, "looking for associated controlplanes", gateway)
	controlplane := new(operatorv1alpha1.ControlPlane)
	controlplaneNSN := types.NamespacedName{Namespace: req.Namespace, Name: "controlplane-" + req.Name} // TODO: generated names https://github.com/Kong/gateway-operator/issues/21
	if err := r.Client.Get(ctx, controlplaneNSN, controlplane); err != nil {
		if k8serrors.IsNotFound(err) {
			return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, r.createControlPlane(ctx, gatewayClass, gateway, gatewayConfig, dataplane.Name)
		}
		return ctrl.Result{}, err
	}

	debug(log, "ensuring controlplane config is up to date", gateway)
	if gatewayConfig.Spec.ControlPlaneDeploymentOptions != nil {
		if !controlplaneSpecDeepEqual(&controlplane.Spec.ControlPlaneDeploymentOptions, gatewayConfig.Spec.ControlPlaneDeploymentOptions) {
			debug(log, "controlplane config is out of date, updating", gateway)
			controlplane.Spec.ControlPlaneDeploymentOptions = *gatewayConfig.Spec.ControlPlaneDeploymentOptions
			return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, r.Client.Update(ctx, controlplane)
		}
	}

	debug(log, "waiting for controlplane readiness", gateway)
	controlplaneReady := false
	for _, condition := range controlplane.Status.Conditions {
		if condition.Type == string(ControlPlaneConditionTypeProvisioned) && condition.Status == metav1.ConditionTrue {
			controlplaneReady = true
		}
	}
	if !controlplaneReady {
		debug(log, "controlplane not ready yet, waiting", gateway)
		return ctrl.Result{Requeue: true, RequeueAfter: requeueWithoutBackoff}, nil
	}

	debug(log, "marking the gateway as ready", gateway)
	if err := r.ensureGatewayMarkedReady(ctx, gateway); err != nil {
		return ctrl.Result{}, err
	}

	debug(log, "successfully reconciled", gateway)
	return ctrl.Result{}, nil
}
