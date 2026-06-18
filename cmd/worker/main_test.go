package main

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/quanluon/checkpoint-app/pkg/pubsub"
)

type fakeCleaner struct {
	deleted []string
}

func (f *fakeCleaner) Delete(_ context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	return nil
}

func TestDispatchJobMediaCleanupSkipsURLsAndEmptyKeys(t *testing.T) {
	payload, err := json.Marshal(pubsub.JobEnvelope{
		Type: pubsub.JobTypeMediaCleanup,
		MediaCleanup: &pubsub.MediaCleanupJob{Keys: []string{
			"",
			"https://cdn.example.com/legacy.jpg",
			"checkpoints/u1/image.jpg",
			"http://cdn.example.com/legacy-thumb.jpg",
			"checkpoints/u1/thumb.jpg",
		}},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	cleaner := &fakeCleaner{}
	if err := dispatchJob(context.Background(), payload, "msg-1", nil, cleaner); err != nil {
		t.Fatalf("dispatchJob error = %v", err)
	}

	want := []string{"checkpoints/u1/image.jpg", "checkpoints/u1/thumb.jpg"}
	if !slices.Equal(cleaner.deleted, want) {
		t.Fatalf("deleted = %v, want %v", cleaner.deleted, want)
	}
}

func TestDispatchJobRejectsUnknownEnvelopeType(t *testing.T) {
	payload, err := json.Marshal(pubsub.JobEnvelope{Type: "unknown"})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	err = dispatchJob(context.Background(), payload, "msg-1", nil, &fakeCleaner{})
	if err == nil {
		t.Fatal("dispatchJob error = nil, want invalidJobError")
	}
	if _, ok := err.(invalidJobError); !ok {
		t.Fatalf("dispatchJob error = %T, want invalidJobError", err)
	}
}
