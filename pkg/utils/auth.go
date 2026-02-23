package utils

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
)

// Secret key names following the CAPV identity pattern.
const (
	UsernameKey = "username"
	PasswordKey = "password"
)

// GetSecret reads a specific data key from a Kubernetes Secret.
func GetSecret(namespace, secret, dataKey string, c client.Client) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()

	var s corev1.Secret
	if err := c.Get(ctx, client.ObjectKey{Name: secret, Namespace: namespace}, &s); err != nil {
		return "", err
	}

	if encodedValue, ok := s.Data[dataKey]; ok {
		return string(encodedValue[:]), nil
	} else {
		return "", fmt.Errorf("secret %s/%s/%s not found", namespace, secret, dataKey)
	}
}

// GetVSphereMetadataFromConfig builds vsphere.Metadata by reading credential
// Secrets referenced from the controller config's vcenters list.
// Each Secret is expected to have "username" and "password" keys.
func GetVSphereMetadataFromConfig(namespace string, vcenters []VCenterConfig, c client.Client) (*vsphere.Metadata, error) {
	metadata := vsphere.NewMetadata()

	for _, vc := range vcenters {
		username, err := GetSecret(namespace, vc.SecretRef, UsernameKey, c)
		if err != nil {
			return nil, fmt.Errorf("failed to read username from secret %s/%s: %w", namespace, vc.SecretRef, err)
		}

		password, err := GetSecret(namespace, vc.SecretRef, PasswordKey, c)
		if err != nil {
			return nil, fmt.Errorf("failed to read password from secret %s/%s: %w", namespace, vc.SecretRef, err)
		}

		if _, err := metadata.AddCredentials(vc.Server, username, password); err != nil {
			return nil, fmt.Errorf("failed to add credentials for server %s: %w", vc.Server, err)
		}
	}

	return metadata, nil
}
