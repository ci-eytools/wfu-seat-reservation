package tui

import (
	"context"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"sort"
	"strings"
	"time"
	"wfuseat/internal/api"
	"wfuseat/internal/config"
	"wfuseat/internal/storage"
)

// jobStore is shared by the local database and authenticated remote API adapter.
type jobStore interface {
	Get(string) ([]byte, error)
	Put(string, []byte) error
	Jobs() ([]storage.Job, error)
	Add(storage.Job) error
	Cancel(string) error
	SetEnabled(bool) error
	Enabled() bool
	Close() error
}

func WithRemote(c *api.Client) Option { return func(m *Model) { m.remote = c } }
func (m *Model) remoteIDs() []string {
	var ids []string
	for id := range m.remote.Profiles() {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
func (m *Model) adoptRemoteIdentity() error {
	identity := m.remote.Identity()
	if !storage.IsServiceAccount(identity.ID) {
		return fmt.Errorf("远程身份未确认")
	}
	if m.cfg.Account != identity.ID {
		old := m.cfg.Account
		if strings.HasPrefix(old, "pending-") {
			_ = storage.RemoveAccount(m.cfg.Root, old)
		}
		if m.addAccountReturn != nil {
			m.addAccountReturn.Close()
			m.addAccountReturn = nil
		}
		cfg, path, e := config.LoadAccount(m.cfg.Root, identity.ID)
		if e != nil {
			return e
		}
		m.cfg = cfg
		m.cfgPath = path
	}
	settings := m.remote.CachedMe().Settings
	m.cfg.AllowSubmit = settings.AllowSubmit
	m.cfg.DelayMS = settings.DelayMS
	m.settings.draft = m.cfg
	_, e := config.Save(m.cfg)
	return e
}

type remoteDeletedMsg struct{ err error }

func (m *Model) remoteDelete() tea.Cmd {
	client := m.remote
	return func() tea.Msg { return remoteDeletedMsg{client.DeleteAccount()} }
}
func (m *Model) onRemoteDeleted(msg remoteDeletedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.pushToast(sevErr, "删除失败：%s", msg.err)
		return m, nil
	}
	ids := m.remoteIDs()
	id := fmt.Sprintf("pending-%d", time.Now().UnixNano())
	if len(ids) > 0 {
		id = ids[0]
	}
	return m.switchAccount(id)
}
func (m *Model) remoteSave(cfg config.Config) tea.Cmd {
	client := m.remote
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		e := client.Settings(ctx, api.Settings{AllowSubmit: cfg.AllowSubmit, DelayMS: cfg.DelayMS})
		path := ""
		if e == nil {
			path, e = config.Save(cfg)
		}
		return configSavedMsg{path: path, err: e, account: cfg.Account, saved: cfg}
	}
}
func (m *Model) remoteAccountLabel() string {
	if strings.HasPrefix(m.cfg.Account, "pending-") {
		return "添加远程账号"
	}
	label := m.remote.Identity().DisplayName
	if label == "" {
		return "远程账号"
	}
	return label
}
