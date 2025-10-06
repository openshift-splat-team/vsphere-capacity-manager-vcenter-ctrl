package vsphere

import (
	"context"
	"fmt"

	"github.com/vmware/govmomi/view"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
)

// VCenterContext maintains context of known vCenters to be used in CAPI manifest reconciliation.
type VCenterContext struct {
	VCenter string
}

// VCenterCredential contains the vCenter username and password.
type VCenterCredential struct {
	Username string
	Password string
}

// Metadata holds vcenter stuff.
type Metadata struct {
	sessions           map[string]*session.Session
	credentials        map[string]*session.Params
	VCenterCredentials map[string]VCenterCredential

	containerView map[string]*view.ContainerView
}

// NewMetadata initializes a new Metadata object.
func NewMetadata() *Metadata {
	return &Metadata{
		sessions:           make(map[string]*session.Session),
		credentials:        make(map[string]*session.Params),
		VCenterCredentials: make(map[string]VCenterCredential),
	}
}

// AddCredentials creates a session param from the vCenter server, username and password
// to the Credentials Map.
func (m *Metadata) AddCredentials(server, username, password string) (*session.Params, error) {
	if _, ok := m.VCenterCredentials[server]; !ok {
		m.VCenterCredentials[server] = VCenterCredential{
			Username: username,
			Password: password,
		}
	}

	// m.credentials is not stored in the json state file - there is no real reason to do this
	// but upon returning to AddCredentials (create manifest, create cluster) the credentials map is
	// nil, re-make it.
	if m.credentials == nil {
		m.credentials = make(map[string]*session.Params)
	}

	if _, ok := m.credentials[server]; !ok {
		m.credentials[server] = session.NewParams().WithServer(server).WithUserInfo(username, password)
	}

	return m.credentials[server], nil
}

// Session returns a session from unlockedSession based on the server (vCenter URL).
func (m *Metadata) Session(ctx context.Context, server string) (*session.Session, error) {
	// m.sessions is not stored in the json state file - there is no real reason to do this
	// but upon returning to Session (create manifest, create cluster) the sessions map is
	// nil, re-make it.
	if m.sessions == nil {
		m.sessions = make(map[string]*session.Session)
	}

	return m.unlockedSession(ctx, server)
}

func (m *Metadata) ContainerView(ctx context.Context, server string) (*view.ContainerView, error) {
	if m.containerView == nil {
		m.containerView = make(map[string]*view.ContainerView)
	}
	return m.unlockedContainerView(ctx, server)
}

func (m *Metadata) unlockedContainerView(ctx context.Context, server string) (*view.ContainerView, error) {
	s, err := m.unlockedSession(ctx, server)
	if err != nil {
		return nil, err
	}

	if _, ok := m.containerView[server]; !ok {
		viewMgr := view.NewManager(s.Client.Client)
		m.containerView[server], err = viewMgr.CreateContainerView(ctx, s.Client.ServiceContent.RootFolder, nil, true)
		if err != nil {
			return nil, err
		}
		return m.containerView[server], nil
	}
	return m.containerView[server], nil
}

func (m *Metadata) unlockedSession(ctx context.Context, server string) (*session.Session, error) {
	var err error
	if _, ok := m.credentials[server]; !ok {
		if c, ok := m.VCenterCredentials[server]; ok {
			_, err := m.AddCredentials(server, c.Username, c.Password)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("credentials for %s not found", server)
		}
	}

	// if nil we haven't created a session
	if _, ok := m.sessions[server]; !ok {
		m.sessions[server], err = session.GetOrCreate(ctx, m.credentials[server])
		if err != nil {
			return nil, err
		}
		return m.sessions[server], nil
	} else if m.sessions[server] == nil {
		m.sessions[server], err = session.GetOrCreate(ctx, m.credentials[server])
		if err != nil {
			return nil, err
		}
		return m.sessions[server], nil
	}

	return m.sessions[server], nil
}
