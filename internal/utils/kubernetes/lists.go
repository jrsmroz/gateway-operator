package kubernetes

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListDeploymentsForOwner is a helper function to map a list of Deployments
// by label and reduce by OwnerReference UID and namespace to efficiently list
// only the objects owned by the provided UID.
func ListDeploymentsForOwner(
	ctx context.Context,
	c client.Client,
	requiredLabel string,
	requiredValue string,
	namespace string,
	uid types.UID,
) ([]appsv1.Deployment, error) {
	deploymentList := &appsv1.DeploymentList{}

	err := c.List(
		ctx,
		deploymentList,
		client.InNamespace(namespace),
		client.MatchingLabels{requiredLabel: requiredValue},
	)
	if err != nil {
		return nil, err
	}

	deployments := make([]appsv1.Deployment, 0)
	for _, deployment := range deploymentList.Items {
		if IsOwnedByRefUID(&deployment.ObjectMeta, uid) {
			deployments = append(deployments, deployment)
		}
	}

	return deployments, nil
}

// ListServicesForOwner is a helper function to map a list of Services
// by label and reduce by OwnerReference UID and namespace to efficiently list
// only the objects owned by the provided UID.
func ListServicesForOwner(
	ctx context.Context,
	c client.Client,
	requiredLabel string,
	requiredValue string,
	namespace string,
	uid types.UID,
) ([]corev1.Service, error) {
	serviceList := &corev1.ServiceList{}

	err := c.List(
		ctx,
		serviceList,
		client.InNamespace(namespace),
		client.MatchingLabels{requiredLabel: requiredValue},
	)
	if err != nil {
		return nil, err
	}

	services := make([]corev1.Service, 0)
	for _, service := range serviceList.Items {
		if IsOwnedByRefUID(&service.ObjectMeta, uid) {
			services = append(services, service)
		}
	}

	return services, nil
}

// ListServiceAccountsForOwner is a helper function to map a list of ServiceAccounts
// by label and reduce by OwnerReference UID and namespace to efficiently list
// only the objects owned by the provided UID.
func ListServiceAccountsForOwner(
	ctx context.Context,
	c client.Client,
	requiredLabel string,
	requiredValue string,
	namespace string,
	uid types.UID,
) ([]corev1.ServiceAccount, error) {
	requirement, err := labels.NewRequirement(
		requiredLabel,
		selection.Equals,
		[]string{requiredValue},
	)
	if err != nil {
		return nil, err
	}
	selector := labels.NewSelector().Add(*requirement)

	listOptions := &client.ListOptions{
		LabelSelector: selector,
	}
	if namespace != "" {
		listOptions.Namespace = namespace
	}

	serviceAccountList := &corev1.ServiceAccountList{}
	if err := c.List(ctx, serviceAccountList, listOptions); err != nil {
		return nil, err
	}

	serviceAccounts := make([]corev1.ServiceAccount, 0)
	for _, serviceAccount := range serviceAccountList.Items {
		for _, ownerRef := range serviceAccount.ObjectMeta.OwnerReferences {
			if ownerRef.UID == uid {
				serviceAccounts = append(serviceAccounts, serviceAccount)
				break
			}
		}
	}

	return serviceAccounts, nil
}

// ListClusterRolesForOwner is a helper function to map a list of ClusterRoles
// by label and reduce by OwnerReference UID and namespace to efficiently list
// only the objects owned by the provided UID.
func ListClusterRolesForOwner(
	ctx context.Context,
	c client.Client,
	requiredLabel string,
	requiredValue string,
	namespace string,
	uid types.UID,
) ([]rbacv1.ClusterRole, error) {
	requirement, err := labels.NewRequirement(
		requiredLabel,
		selection.Equals,
		[]string{requiredValue},
	)
	if err != nil {
		return nil, err
	}
	selector := labels.NewSelector().Add(*requirement)

	listOptions := &client.ListOptions{
		LabelSelector: selector,
	}

	clusterRoleList := &rbacv1.ClusterRoleList{}
	if err := c.List(ctx, clusterRoleList, listOptions); err != nil {
		return nil, err
	}

	clusterRoles := make([]rbacv1.ClusterRole, 0)
	for _, clusterRole := range clusterRoleList.Items {
		for _, ownerRef := range clusterRole.ObjectMeta.OwnerReferences {
			if ownerRef.UID == uid {
				clusterRoles = append(clusterRoles, clusterRole)
				break
			}
		}
	}

	return clusterRoles, nil
}

// ListClusterRoleBindingsForOwner is a helper function to map a list of ClusterRoleBindings
// by label and reduce by OwnerReference UID and namespace to efficiently list
// only the objects owned by the provided UID.
func ListClusterRoleBindingsForOwner(
	ctx context.Context,
	c client.Client,
	requiredLabel string,
	requiredValue string,
	namespace string,
	uid types.UID,
) ([]rbacv1.ClusterRoleBinding, error) {
	requirement, err := labels.NewRequirement(
		requiredLabel,
		selection.Equals,
		[]string{requiredValue},
	)
	if err != nil {
		return nil, err
	}
	selector := labels.NewSelector().Add(*requirement)

	listOptions := &client.ListOptions{
		LabelSelector: selector,
	}

	clusterRoleBindingList := &rbacv1.ClusterRoleBindingList{}
	if err := c.List(ctx, clusterRoleBindingList, listOptions); err != nil {
		return nil, err
	}

	clusterRoleBindings := make([]rbacv1.ClusterRoleBinding, 0)
	for _, clusterRoleBinding := range clusterRoleBindingList.Items {
		for _, ownerRef := range clusterRoleBinding.ObjectMeta.OwnerReferences {
			if ownerRef.UID == uid {
				clusterRoleBindings = append(clusterRoleBindings, clusterRoleBinding)
				break
			}
		}
	}

	return clusterRoleBindings, nil
}
