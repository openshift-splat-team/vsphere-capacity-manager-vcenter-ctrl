/*
Copyright 2024.

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

package utils

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to build scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func TestGetSecret_ReturnsDataKey(t *testing.T) {
	c := newFakeClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-creds", Namespace: "vcm"},
		Data:       map[string][]byte{"username": []byte("admin@vsphere.local")},
	})

	value, err := GetSecret("vcm", "vc-creds", "username", c)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if value != "admin@vsphere.local" {
		t.Errorf("expected username, got %q", value)
	}
}

func TestGetSecret_MissingSecretOrKeyErrors(t *testing.T) {
	c := newFakeClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-creds", Namespace: "vcm"},
		Data:       map[string][]byte{"username": []byte("admin")},
	})

	if _, err := GetSecret("vcm", "vc-creds", "password", c); err == nil {
		t.Error("expected an error for a missing data key")
	}
	if _, err := GetSecret("vcm", "missing-secret", "username", c); err == nil {
		t.Error("expected an error for a missing secret")
	}
}

func TestGetVSphereMetadataFromConfig_RegistersCredentialsPerServer(t *testing.T) {
	c := newFakeClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-creds", Namespace: "vcm"},
		Data: map[string][]byte{
			"username": []byte("user"),
			"password": []byte("pass"),
		},
	})

	md, err := GetVSphereMetadataFromConfig("vcm", []VCenterConfig{
		{Server: "https://vcenter.one", SecretRef: "vc-creds"},
		{Server: "https://vcenter.two", SecretRef: "vc-creds"},
	}, c)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if _, ok := md.VCenterCredentials["https://vcenter.one"]; !ok {
		t.Error("expected credentials registered for the first server")
	}
	if _, ok := md.VCenterCredentials["https://vcenter.two"]; !ok {
		t.Error("expected credentials registered for the second server")
	}
}

func TestGetVSphereMetadataFromConfig_MissingSecretErrors(t *testing.T) {
	c := newFakeClient(t)
	if _, err := GetVSphereMetadataFromConfig("vcm", []VCenterConfig{{Server: "s", SecretRef: "nope"}}, c); err == nil {
		t.Error("expected an error when the credential secret is missing")
	}
}

func TestReadControllerConfig_ParsesConfigMapAndAppliesDefaults(t *testing.T) {
	c := newFakeClient(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-config", Namespace: "vcm"},
		Data: map[string]string{
			"config.yaml": `
vcenters:
  - server: vcenter.example.com
    secretRef: vc-creds
`,
		},
	})

	config, err := ReadControllerConfig("vcm", "controller-config", c)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(config.VCenters) != 1 || config.VCenters[0].Server != "vcenter.example.com" {
		t.Errorf("unexpected vcenters: %+v", config.VCenters)
	}
	// defaults from applyDefaults
	if config.Safety.MinAgeHours != 2 {
		t.Errorf("expected default MinAgeHours of 2, got %d", config.Safety.MinAgeHours)
	}
}

func TestReadControllerConfig_RejectsMissingKeyUnparsableAndEmpty(t *testing.T) {
	missingKey := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-config", Namespace: "vcm"},
		Data:       map[string]string{"other": "x"},
	}
	if _, err := ReadControllerConfig("vcm", "controller-config", newFakeClient(t, missingKey)); err == nil {
		t.Error("expected an error when config.yaml is missing")
	}

	badYAML := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-config", Namespace: "vcm"},
		Data:       map[string]string{"config.yaml": "vcenters: [unclosed"},
	}
	if _, err := ReadControllerConfig("vcm", "controller-config", newFakeClient(t, badYAML)); err == nil {
		t.Error("expected an error for unparsable yaml")
	}

	noVCenters := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "controller-config", Namespace: "vcm"},
		Data:       map[string]string{"config.yaml": "safety: {}"},
	}
	if _, err := ReadControllerConfig("vcm", "controller-config", newFakeClient(t, noVCenters)); err == nil {
		t.Error("expected an error when no vcenters are configured")
	}
}
