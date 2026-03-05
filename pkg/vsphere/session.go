package vsphere

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/vmware/govmomi/view"
	"sigs.k8s.io/cluster-api-provider-vsphere/pkg/session"
)

const sessionTimeout = 120 * time.Second

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
	// mu protects the sessions, credentials, and VCenterCredentials maps,
	// as well as the serverMu map. Hold briefly to read/write map entries;
	// never hold across network calls.
	mu sync.Mutex

	// serverMu provides per-server mutexes so that session creation for
	// one vCenter does not block session creation for another.
	serverMu map[string]*sync.Mutex

	sessions           map[string]*session.Session
	credentials        map[string]*session.Params
	VCenterCredentials map[string]VCenterCredential

	containerView map[string]*view.ContainerView
}

// NewMetadata initializes a new Metadata object.
func NewMetadata() *Metadata {
	return &Metadata{
		serverMu:           make(map[string]*sync.Mutex),
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

// getCredentialsLocked returns session.Params for the given server, building
// them from VCenterCredentials if they don't already exist.
// Caller must hold m.mu.
func (m *Metadata) getCredentialsLocked(server string) (*session.Params, error) {
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

	return m.credentials[server], nil
}

// serverMuLocked returns the per-server mutex, creating one if needed.
// Caller must hold m.mu.
func (m *Metadata) serverMuLocked(server string) *sync.Mutex {
	if m.serverMu == nil {
		m.serverMu = make(map[string]*sync.Mutex)
	}
	mu, ok := m.serverMu[server]
	if !ok {
		mu = &sync.Mutex{}
		m.serverMu[server] = mu
	}
	return mu
}

// Session returns a session for the given vCenter server.
// Uses per-server locking so that a slow vCenter does not block other servers,
// and releases the global mutex before making network calls to avoid holding
// it during potentially long session.GetOrCreate operations.
func (m *Metadata) Session(ctx context.Context, server string) (*session.Session, error) {
	// Phase 1: Read credentials and get the per-server mutex under the global lock.
	m.mu.Lock()

	if m.sessions == nil {
		m.sessions = make(map[string]*session.Session)
	}

	params, err := m.getCredentialsLocked(server)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}

	smu := m.serverMuLocked(server)
	m.mu.Unlock()

	// Phase 2: Acquire the per-server lock and perform the (potentially slow)
	// network call to GetOrCreate without holding the global lock.
	smu.Lock()
	defer smu.Unlock()

	timeoutCtx, cancel := context.WithTimeout(ctx, sessionTimeout)
	defer cancel()

	s, err := session.GetOrCreate(timeoutCtx, params)
	if err != nil {
		return nil, fmt.Errorf("session for %s: %w", server, err)
	}

	// Phase 3: Store the session result under the global lock.
	m.mu.Lock()
	m.sessions[server] = s
	m.mu.Unlock()

	return s, nil
}

// ContainerView creates a new container view for the given vCenter server.
// Each call creates a fresh view; callers must destroy it via DestroyContainerViews.
func (m *Metadata) ContainerView(ctx context.Context, server string) (*view.ContainerView, error) {
	s, err := m.Session(ctx, server)
	if err != nil {
		return nil, err
	}

	viewMgr := view.NewManager(s.Client.Client)
	return viewMgr.CreateContainerView(ctx, s.Client.ServiceContent.RootFolder, nil, true)
}

// DestroyContainerViews destroys a list of container views.
func (m *Metadata) DestroyContainerViews(ctx context.Context, containerViews []*view.ContainerView) error {
	for _, v := range containerViews {
		if err := v.Destroy(ctx); err != nil {
			return err
		}
	}

	return nil
}
