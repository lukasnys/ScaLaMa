package main

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

/*
Checks whether the read-namespaces-cr ClusterRole exists.
*/
func readNamespaceClusterRoleExists(clienset *kubernetes.Clientset) (bool, error) {
	_, err := clientset.RbacV1().ClusterRoles().Get(context.TODO(), "read-namespaces-cr", v1.GetOptions{})
	if err != nil {
		if strings.HasSuffix(err.Error(), "not found") {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

/*
Creates the read-namespaces-cr ClusterRole. This ClusterRole defines permissions to "list" and "get" namespaces.
*/
func createReadNamespacesClusterRole(clientset *kubernetes.Clientset) error {
	clusterRole := &rbacv1.ClusterRole{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "read-namespaces-cr",
		},
		Rules: []rbacv1.PolicyRule{
			0: {
				APIGroups: []string{""},
				Verbs:     []string{"get", "list"},
				Resources: []string{"namespaces"},
			},
		},
	}

	if _, err := clientset.RbacV1().ClusterRoles().Create(context.TODO(), clusterRole, v1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

/*
Creates a ClusterRoleBinding for the read-namespaces-cr ClusterRole. Binds the permissions to a ServiceAccount defined by username and namespace.
The labName parameter is used to ensure the uniqueness of the ClusterRoleBinding name.
*/
func createReadNamespacesClusterRoleBinding(clientset *kubernetes.Clientset, labName string, username string, namespace string) error {
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      "read-namespaces-crb-" + labName + "-" + username,
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			0: {
				Kind:      "ServiceAccount",
				Name:      username,
				Namespace: namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			Name:     "read-namespaces-cr",
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if _, err := clientset.RbacV1().ClusterRoleBindings().Create(context.TODO(), clusterRoleBinding, v1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

/*
Creates a Role with a name inside of a namespace with the permissions defined in the verbs paramter on all resources of all APIGroups.
*/
func createRole(clientset *kubernetes.Clientset, name string, namespace string, verbs []string) error {
	role := &rbacv1.Role{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "Role",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Rules: []rbacv1.PolicyRule{
			0: {
				APIGroups: []string{"*"},
				Verbs:     verbs,
				Resources: []string{"*"},
			},
		},
	}

	if _, err := clientset.RbacV1().Roles(namespace).Create(context.TODO(), role, v1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

/*
Creates a RoleBinding with a name inside of a namespace. Binds the permissions of roleName to a ServiceAccount with username inside of userNamespace.
*/
func createRoleBinding(clientset *kubernetes.Clientset, name string, namespace string, username string, userNamespace string, roleName string) error {
	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: v1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "RoleBinding",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Subjects: []rbacv1.Subject{
			0: {
				Kind:      "ServiceAccount",
				Name:      username,
				Namespace: userNamespace,
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "Role",
			Name:     roleName,
			APIGroup: "rbac.authorization.k8s.io",
		},
	}

	if _, err := clientset.RbacV1().RoleBindings(namespace).Create(context.TODO(), roleBinding, v1.CreateOptions{}); err != nil {
		return err
	}

	return nil
}

/*
Creates a ServiceAccount with a username inside of a namespace.
Returns the Secret token for that ServiceAccount.
*/
func createServiceAccount(clientset *kubernetes.Clientset, username string, namespace string) (string, error) {
	serviceAccount := &corev1.ServiceAccount{
		TypeMeta: v1.TypeMeta{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
		},
		ObjectMeta: v1.ObjectMeta{
			Name:      username,
			Namespace: namespace,
		},
	}

	serviceAccount, err := clientset.CoreV1().ServiceAccounts(namespace).Create(context.TODO(), serviceAccount, v1.CreateOptions{})
	if err != nil {
		return "", err
	}

	for {
		serviceAccount, err = clientset.CoreV1().ServiceAccounts(namespace).Get(context.TODO(), serviceAccount.GetName(), v1.GetOptions{})
		if err != nil {
			return "", err
		}

		if len(serviceAccount.Secrets) > 0 {
			break
		}
	}

	secretName := serviceAccount.Secrets[0].Name
	secret, err := clientset.CoreV1().Secrets(namespace).Get(context.TODO(), secretName, v1.GetOptions{})
	if err != nil {
		return "", err
	}

	return string(secret.Data["token"][:]), nil
}
