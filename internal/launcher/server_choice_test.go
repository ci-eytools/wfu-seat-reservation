package launcher

import (
	"context"
	tea "github.com/charmbracelet/bubbletea"
	"testing"
)

func TestRememberServerAndSwitch(t *testing.T) {
	root := t.TempDir()
	if got, e := SavedServer(root); e != nil || got != "" {
		t.Fatal(got, e)
	}
	got, e := ResolveServer(context.Background(), root, "https://EXAMPLE.com/", false, true)
	if e != nil || got != "https://example.com" {
		t.Fatal(got, e)
	}
	got, e = ResolveServer(context.Background(), root, "", false, true)
	if e != nil || got != "https://example.com" {
		t.Fatal("address not restored", got, e)
	}
	if _, e = RememberServer(root, "http://public.example"); e == nil {
		t.Fatal("invalid address saved")
	}
	got, e = SavedServer(root)
	if e != nil || got != "https://example.com" {
		t.Fatal("previous address overwritten")
	}
	if _, e = RememberServer(root, "http://127.0.0.1:9000"); e != nil {
		t.Fatal(e)
	}
	got, e = SavedServer(root)
	if e != nil || got != "http://127.0.0.1:9000" {
		t.Fatal("switch failed")
	}
}
func TestServerChoiceValidationAndCancellation(t *testing.T) {
	model := newServerChoice("bad")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	invalid := updated.(serverChoice)
	if invalid.err == "" || invalid.selected != "" {
		t.Fatal("invalid URL accepted")
	}
	invalid.input.SetValue("http://127.0.0.1:8787/")
	updated, _ = invalid.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(serverChoice).selected != "http://127.0.0.1:8787" {
		t.Fatal("valid URL refused")
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !updated.(serverChoice).cancelled {
		t.Fatal("cancel failed")
	}
	if _, e := ResolveServer(context.Background(), t.TempDir(), "", false, true); e == nil {
		t.Fatal("query attempted interactive setup")
	}
}
