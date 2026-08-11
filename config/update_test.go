package config

import (
	"sync"
	"testing"
)

func TestUpdateSerializesConcurrentReadModifyWrite(t *testing.T) {
	t.Setenv("WECLAW_HOME", t.TempDir())
	base := DefaultConfig()
	base.Platforms["feishu"] = PlatformConfig{Bots: []FeishuBotConfig{
		{Name: "main", AppID: "cli_a"},
		{Name: "android", AppID: "cli_b"},
	}}
	if err := Save(base); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	updateBot := func(index int, identity string) {
		defer ready.Done()
		<-start
		errs <- Update(func(cfg *Config) error {
			platformCfg := cfg.Platforms["feishu"]
			platformCfg.Bots[index].AllowedUsers = append(platformCfg.Bots[index].AllowedUsers, identity)
			cfg.Platforms["feishu"] = platformCfg
			return nil
		})
	}
	go updateBot(0, "on_user_a")
	go updateBot(1, "on_user_b")
	close(start)
	ready.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	bots := loaded.Platforms["feishu"].Bots
	if len(bots) != 2 || len(bots[0].AllowedUsers) != 1 || bots[0].AllowedUsers[0] != "on_user_a" ||
		len(bots[1].AllowedUsers) != 1 || bots[1].AllowedUsers[0] != "on_user_b" {
		t.Fatalf("concurrent updates lost data: %#v", bots)
	}
}
