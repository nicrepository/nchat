package domain

import (
	"errors"
	"time"
)

var (
	ErrInvalidInput = errors.New("invalid input")
	ErrUnauthorized = errors.New("unauthorized")
)

type MessageResult struct {
	ID                string    `json:"id"`
	ChannelID         string    `json:"channel_id"`
	ChannelName       string    `json:"channel_name"`
	SenderID          string    `json:"sender_id"`
	SenderDisplayName string    `json:"sender_display_name"`
	BodyText          string    `json:"body_text"`
	CreatedAt         time.Time `json:"created_at"`
	Score             float64   `json:"score"`
}
type UserResult struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	AvatarURL   *string `json:"avatar_url,omitempty"`
	SortName    string  `json:"-"`
}
type ChannelResult struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	IsGeneral   bool   `json:"is_general"`
	SortName    string `json:"-"`
}
type MessagePage struct {
	Items      []MessageResult
	NextCursor string
}
type UserPage struct {
	Items      []UserResult
	NextCursor string
}
type ChannelPage struct {
	Items      []ChannelResult
	NextCursor string
}
