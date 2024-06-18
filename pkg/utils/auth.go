package utils

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/openshift-splat-team/vsphere-capacity-manager-vcenter-ctrl/pkg/vsphere"
)

const (
	vcenterPasswordsKey = "vcenter_passwords"
	vcenterUsernamesKey = "vcenter_usernames"
)

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

func parseAuthShellScript(reader io.Reader) (username, password string, err error) {
	r, err := interp.New()
	if err != nil {
		return "", "", err
	}

	f, err := syntax.NewParser().Parse(reader, "")
	if err != nil {
		return "", "", err
	}

	if err := r.Run(context.Background(), f); err != nil {
		return "", "", err
	}

	if usernames, ok := r.Vars[vcenterUsernamesKey]; ok {
		if len(usernames.List) > 0 {
			username = usernames.List[0]
		}
	}
	if passwords, ok := r.Vars[vcenterPasswordsKey]; ok {
		if len(passwords.List) > 0 {
			password = passwords.List[0]
		}
	}

	if username == "" || password == "" {
		return "", "", fmt.Errorf("unable to parse auth shell script, no username or password set")
	}

	return username, password, nil
}

func GetVSphereMetadataBySecretString(namespace, secretNames, secretDatakeys, secretVCenters string, c client.Client) (*vsphere.Metadata, error) {
	metadata := vsphere.NewMetadata()
	secrets := strings.Split(secretNames, ",")
	datakeys := strings.Split(secretDatakeys, ",")
	servers := strings.Split(secretVCenters, ",")

	if len(secrets) != len(datakeys) {
		return nil, fmt.Errorf("command line does not have enough arguments for secret names and data keys")
	}

	if len(secrets) != len(servers) {
		return nil, fmt.Errorf("command line does not have enough arguments for secret names and vcenters")
	}

	for i, s := range secrets {
		secretString, err := GetSecret(namespace, s, datakeys[i], c)
		if err != nil {
			return nil, err
		}

		username, password, err := parseAuthShellScript(strings.NewReader(secretString))

		if err != nil {
			return nil, err
		}
		_, err = metadata.AddCredentials(servers[i], username, password)
		if err != nil {
			return nil, err
		}
	}
	return metadata, nil
}
