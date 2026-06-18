package share

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quanluon/checkpoint-app/internal/checkpoint"
	"github.com/quanluon/checkpoint-app/internal/identity"
	"github.com/quanluon/checkpoint-app/internal/journey"
	"github.com/quanluon/checkpoint-app/internal/place"
)

type mockCheckpointService struct {
	detail *checkpoint.Detail
	err    error
}

func (m *mockCheckpointService) GetDetail(ctx context.Context, id string) (*checkpoint.Detail, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.detail, nil
}

type mockJourneyService struct {
	detail *journey.Detail
	err    error
}

func (m *mockJourneyService) GetDetail(ctx context.Context, id string) (*journey.Detail, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.detail, nil
}

type mockIdentityService struct {
	user     *identity.User
	userByID *identity.User
	err      error
}

func (m *mockIdentityService) GetByUsername(ctx context.Context, username string) (*identity.User, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.user, nil
}

func (m *mockIdentityService) GetByID(ctx context.Context, id string) (*identity.User, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.userByID, nil
}

type mockPlaceService struct {
	detail *place.Detail
	err    error
}

func (m *mockPlaceService) GetDetail(ctx context.Context, id string, limit int, offset int, viewerID *string) (*place.Detail, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.detail, nil
}

func TestGetCheckpointMeta(t *testing.T) {
	displayName := "Nguyen Van A"
	note := "Di phuot vui qua!"
	cpDetail := &checkpoint.Detail{
		Checkpoint: &checkpoint.Checkpoint{
			Name: "Vung Tau Beach",
			Note: &note,
		},
		Author: &checkpoint.Author{
			Username:    "nva",
			DisplayName: &displayName,
		},
		Images: []checkpoint.Image{
			{URL: "https://r2.dev/img1.jpg"},
		},
	}

	mockCP := &mockCheckpointService{detail: cpDetail}
	mockJourney := &mockJourneyService{}
	mockIdentity := &mockIdentityService{}

	svc := NewService(mockCP, mockJourney, mockIdentity, &mockPlaceService{})

	meta, err := svc.GetCheckpointMeta(context.Background(), "cp-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Title != "Checkpoint tại Vung Tau Beach bởi Nguyen Van A" {
		t.Errorf("wrong title: %s", meta.Title)
	}
	if meta.Description != "Di phuot vui qua!" {
		t.Errorf("wrong description: %s", meta.Description)
	}
	if meta.Image != "https://r2.dev/img1.jpg" {
		t.Errorf("wrong image: %s", meta.Image)
	}
	if meta.RedirectURL != "https://checkpoint.app/c/cp-1" {
		t.Errorf("wrong redirect url: %s", meta.RedirectURL)
	}
}

func TestGetJourneyMeta(t *testing.T) {
	journeyDetail := &journey.Detail{
		Journey: &journey.Journey{
			Title:  "Tay Bac Trip",
			UserID: "user-1",
		},
		Stats: journey.Stats{
			CheckpointCount: 5,
			TotalXP:         50,
			DistanceKm:      150.5,
		},
	}

	mockCP := &mockCheckpointService{}
	mockJourney := &mockJourneyService{detail: journeyDetail}
	displayName := "Biker A"
	mockIdentity := &mockIdentityService{
		userByID: &identity.User{
			Username:    "bikera",
			DisplayName: &displayName,
		},
	}

	svc := NewService(mockCP, mockJourney, mockIdentity, &mockPlaceService{})

	meta, err := svc.GetJourneyMeta(context.Background(), "j-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Title != "Tay Bac Trip - Hành trình của Biker A" {
		t.Errorf("wrong title: %s", meta.Title)
	}
	if meta.Description != "Khám phá hành trình gồm 5 địa điểm, tích lũy 50 XP, tổng quãng đường di chuyển 150.5 km trên Checkpoint." {
		t.Errorf("wrong description: %s", meta.Description)
	}
}

func TestGetUserMeta(t *testing.T) {
	displayName := "Phuot Thu"
	bio := "Di va trai nghiem"
	avatar := "https://r2.dev/avatar.jpg"
	user := &identity.User{
		Username:    "phuotthu",
		DisplayName: &displayName,
		Bio:         &bio,
		AvatarURL:   &avatar,
	}

	mockCP := &mockCheckpointService{}
	mockJourney := &mockJourneyService{}
	mockIdentity := &mockIdentityService{user: user}

	svc := NewService(mockCP, mockJourney, mockIdentity, &mockPlaceService{})

	meta, err := svc.GetUserMeta(context.Background(), "phuotthu", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Title != "Phuot Thu (@phuotthu) trên Checkpoint" {
		t.Errorf("wrong title: %s", meta.Title)
	}
	if meta.Description != "Di va trai nghiem" {
		t.Errorf("wrong description: %s", meta.Description)
	}
	if meta.Image != "https://r2.dev/avatar.jpg" {
		t.Errorf("wrong image: %s", meta.Image)
	}
}

func TestGetCheckpointMeta_NotFound(t *testing.T) {
	mockCP := &mockCheckpointService{err: errors.New("not found")}
	svc := NewService(mockCP, &mockJourneyService{}, &mockIdentityService{}, &mockPlaceService{})

	_, err := svc.GetCheckpointMeta(context.Background(), "cp-invalid", "https://checkpoint.app")
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestGetPlaceMeta(t *testing.T) {
	address := "123 Le Loi, Vung Tau"
	placeDetail := &place.Detail{
		Place: &place.Place{
			Name:         "Vung Tau Lighthouse",
			CheckinCount: 15,
			Address:      &address,
		},
		Images: []place.DetailImage{
			{URL: "https://r2.dev/place1.jpg"},
		},
	}

	mockCP := &mockCheckpointService{}
	mockJourney := &mockJourneyService{}
	mockIdentity := &mockIdentityService{}
	mockPlace := &mockPlaceService{detail: placeDetail}

	svc := NewService(mockCP, mockJourney, mockIdentity, mockPlace)

	meta, err := svc.GetPlaceMeta(context.Background(), "place-1", "https://checkpoint.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if meta.Title != "Vung Tau Lighthouse" {
		t.Errorf("wrong title: %s", meta.Title)
	}
	if !strings.Contains(meta.Description, "Khám phá địa điểm du lịch Vung Tau Lighthouse") {
		t.Errorf("wrong description: %s", meta.Description)
	}
	if meta.Image != "https://r2.dev/place1.jpg" {
		t.Errorf("wrong image: %s", meta.Image)
	}
	if meta.RedirectURL != "https://checkpoint.app/p/place-1" {
		t.Errorf("wrong redirect url: %s", meta.RedirectURL)
	}
}
