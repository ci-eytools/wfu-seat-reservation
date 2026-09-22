package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"sort"
	"time"
	"wfuseat/internal/api"
	"wfuseat/internal/config"
	"wfuseat/internal/tui"
)

// runRemote never starts a local scheduler: the selected backend owns execution.
func Remote(ctx context.Context, root, endpoint, selected string, list, jobs bool) error {
	client, err := api.NewClient(endpoint, root)
	if err != nil {
		return err
	}
	profiles := client.Profiles()
	ids := make([]string, 0, len(profiles))
	for id := range profiles {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if list {
		for _, id := range ids {
			fmt.Printf("%s  %s\n", id, profiles[id])
		}
		return nil
	}
	if selected != "" {
		if _, ok := profiles[selected]; !ok {
			match := ""
			for id, label := range profiles {
				if label == selected {
					if match != "" {
						return fmt.Errorf("同名账号，请使用账号 ID")
					}
					match = id
				}
			}
			if match == "" {
				return fmt.Errorf("此后端尚未保存该账号，请扫码新增")
			}
			selected = match
		}
	} else if len(ids) > 0 {
		selected = ids[0]
	}
	if selected != "" {
		if err = client.LoadCredentials(selected); err != nil {
			return err
		}
	} else {
		if jobs {
			return fmt.Errorf("尚未登录此后端")
		}
		selected = fmt.Sprintf("pending-%d", time.Now().UnixNano())
	}
	if jobs {
		items, e := client.Jobs()
		if e != nil {
			return e
		}
		b, e := json.MarshalIndent(items, "", "  ")
		if e != nil {
			return e
		}
		fmt.Println(string(b))
		return nil
	}
	cfg, path, err := config.LoadAccount(client.ProfileRoot, selected)
	if err != nil {
		return err
	}
	model, err := tui.New(cfg, path, tui.WithRemote(client))
	if err != nil {
		return err
	}
	defer model.Close()
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := program.Run()
	if last, ok := final.(*tui.Model); ok && last != model {
		last.Close()
	}
	if err != nil {
		return fmt.Errorf("TUI 运行失败: %w", err)
	}
	return nil
}
