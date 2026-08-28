package tty

import (
	"context"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/morzecrew/morzer/internal/events"
)

// NewWatchModel builds the watch view without the program and the alt-screen
// Watch owns, so a test can drive it with no terminal.
func NewWatchModel[T any](ctx context.Context, opts WatchOptions[T]) tea.Model {
	return newWatchModel(ctx, opts)
}

// EventMsg wraps an event as a Bubble Tea message, so a test can drive the
// model from an event stream directly -- the same path Subscribe takes.
func EventMsg(e events.Event) tea.Msg { return eventMsg{event: e} }
