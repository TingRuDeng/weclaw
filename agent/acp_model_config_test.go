package agent

import (
	"fmt"
	"sync"
	"testing"
)

func TestACPAgentConcurrentModelUpdatesAndInfo(t *testing.T) {
	agent := NewACPAgent(ACPAgentConfig{Command: "codex", Model: "model-a", Effort: "effort-a"})
	const iterations = 2000
	start := make(chan struct{})
	errs := make(chan error, 4)
	var workers sync.WaitGroup

	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < iterations; index++ {
			agent.SetCodexModel("model-a", "effort-a")
			agent.SetCodexModel("model-b", "effort-b")
		}
	}()
	for worker := 0; worker < 3; worker++ {
		workers.Add(1)
		go readACPModelInfoForTest(agent, start, iterations, &workers, errs)
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func readACPModelInfoForTest(agent *ACPAgent, start <-chan struct{}, iterations int, workers *sync.WaitGroup, errs chan<- error) {
	defer workers.Done()
	<-start
	for index := 0; index < iterations; index++ {
		info := agent.Info()
		if info.Model == "model-a" && info.Effort == "effort-a" || info.Model == "model-b" && info.Effort == "effort-b" {
			continue
		}
		errs <- fmt.Errorf("observed mixed model config: model=%q effort=%q", info.Model, info.Effort)
		return
	}
}
