package vsphere

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vmware/govmomi/view"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
)

const sessionTimeout = 60 * time.Second

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
	mu sync.RWMutex

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
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.addCredentialsLocked(server, username, password)
}

// addCredentialsLocked is the internal implementation of AddCredentials.
// Caller must hold m.mu.
func (m *Metadata) addCredentialsLocked(server, username, password string) (*session.Params, error) {
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
	m.mu.Lock()
	defer m.mu.Unlock()

	// m.sessions is not stored in the json state file - there is no real reason to do this
	// but upon returning to Session (create manifest, create cluster) the sessions map is
	// nil, re-make it.
	if m.sessions == nil {
		m.sessions = make(map[string]*session.Session)
	}

	return m.sessionLocked(ctx, server)
}

func (m *Metadata) ContainerView(ctx context.Context, server string) (*view.ContainerView, error) {
	m.mu.Lock()
	s, err := m.sessionLocked(ctx, server)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}

	viewMgr := view.NewManager(s.Client.Client)
	return viewMgr.CreateContainerView(ctx, s.Client.ServiceContent.RootFolder, nil, true)
}

func (m *Metadata) DestroyContainerViews(ctx context.Context, containerViews []*view.ContainerView) error {
	for _, v := range containerViews {
		if err := v.Destroy(ctx); err != nil {
			return err
		}
	}

	return nil
}

// sessionLocked gets or creates a session for the given server.
// Caller must hold m.mu.
func (m *Metadata) sessionLocked(ctx context.Context, server string) (*session.Session, error) {
	var err error
	if _, ok := m.credentials[server]; !ok {
		if c, ok := m.VCenterCredentials[server]; ok {
			_, err := m.addCredentialsLocked(server, c.Username, c.Password)
			if err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("credentials for %s not found", server)
		}
	}

	// Apply a timeout to prevent a single unresponsive vCenter from blocking
	// the global session mutex indefinitely.
	timeoutCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	// We are going to keep this simple since session is
	// caching the session anyway. Always set the m.sessions[server]
	m.sessions[server], err = session.GetOrCreate(timeoutCtx, m.credentials[server])
	if err != nil {
		return nil, fmt.Errorf("session for %s: %w", server, err)
	}

	return m.sessions[server], nil
}
