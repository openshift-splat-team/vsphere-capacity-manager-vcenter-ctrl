package utils

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// ControllerConfig holds the parsed configuration from the controller ConfigMap.
type ControllerConfig struct {
	VCenters []VCenterConfig `json:"vcenters"`
}

// VCenterConfig maps a vCenter server to its credential Secret.
type VCenterConfig struct {
	Server    string `json:"server"`
	SecretRef string `json:"secretRef"`
}

// ReadControllerConfig reads and parses the controller ConfigMap into a ControllerConfig.
func ReadControllerConfig(namespace, configMapName string, c client.Client) (*ControllerConfig, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()

	var cm corev1.ConfigMap
	if err := c.Get(ctx, client.ObjectKey{Name: configMapName, Namespace: namespace}, &cm); err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	configData, ok := cm.Data["config.yaml"]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s/%s missing 'config.yaml' key", namespace, configMapName)
	}

	var config ControllerConfig
	if err := yaml.Unmarshal([]byte(configData), &config); err != nil {
		return nil, fmt.Errorf("failed to parse config.yaml from ConfigMap %s/%s: %w", namespace, configMapName, err)
	}

	if len(config.VCenters) == 0 {
		return nil, fmt.Errorf("no vcenters configured in ConfigMap %s/%s", namespace, configMapName)
	}

	return &config, nil
}
