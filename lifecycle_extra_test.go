package gori

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCloneCopiesResponseModalities(t *testing.T) {
	a := &Agent{ResponseModalities: []string{"audio"}}
	c := a.Clone()
	a.ResponseModalities[0] = "image"
	if c.ResponseModalities[0] != "audio" {
		t.Errorf("clone shares the modalities slice: %v", c.ResponseModalities)
	}
}

func TestBusDroppedCounter(t *testing.T) {
	b := NewBus()
	defer b.Close()
	_, unsub := b.Subscribe("*")
	defer unsub()
	for i := 0; i < 200; i++ {
		b.Publish(context.Background(), Event{Topic: "t", Kind: "k"})
	}
	if b.Dropped() == 0 {
		t.Error("expected drops with an undrained subscriber")
	}
}

func TestOrchestratorUsageAggregation(t *testing.T) {
	sub := &Agent{
		Provider: &fakeProvider{responses: []Response{
			{Message: AssistantText("done"), StopReason: StopEndTurn, Usage: Usage{InputTokens: 11, OutputTokens: 4}},
		}},
		Model:   "x",
		Session: NewSession(),
	}
	o := NewOrchestrator(nil)
	o.Add("worker", "sub", sub)
	tool := o.AsTool("worker", "do work")
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"task":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if u := o.Usage(); u.InputTokens != 11 || u.OutputTokens != 4 {
		t.Errorf("Usage = %+v, want In:11 Out:4", u)
	}
}
