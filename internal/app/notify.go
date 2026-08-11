package app

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/services/notifications"

	"github.com/artipop/xxvi/internal/acp"
)

// A question an agent asked reaches a person who is not looking at the
// application. It carries its own options, so it can be answered from the
// notification — the panel and the card show the same question, and answering
// any of the three lets the agent go on.
//
// The notification is a second road to the answer, never the only one: a
// machine that refuses notification permission, or a platform that drops the
// action buttons, loses nothing that the panel does not still have.

// Notifier turns open questions into system notifications and answers back.
type Notifier struct {
	app     *App
	service *notifications.NotificationService

	mu    sync.Mutex
	shown map[string]bool // question id → a notification is out for it
	ok    bool            // notifications are permitted
}

// NewNotifier wires the notification service to the questions. It asks for
// permission once; a refusal is not an error worth stopping for — the question
// is still on its card and in the panel.
//
// Only call it when NotificationsPossible said so: the service itself refuses
// to start without a bundle, and that refusal stops the whole application.
func NewNotifier(a *App, service *notifications.NotificationService) *Notifier {
	n := &Notifier{app: a, service: service, shown: map[string]bool{}}
	granted, err := service.RequestNotificationAuthorization()
	if err != nil {
		a.log.Info("уведомления недоступны, вопросы видны только в приложении", "err", err)
	}
	n.ok = granted && err == nil
	service.OnNotificationResponse(n.onResponse)
	return n
}

// NotificationsPossible reports whether system notifications can be used here
// at all, and why not when they cannot.
//
// It has to be asked *before* the notification service is registered, not
// after: on macOS the service refuses to start without a bundle identifier and
// that refusal stops the application, while UNUserNotificationCenter aborts the
// process outright — not returns an error, aborts. A bare binary is exactly how
// this runs during development, so both would mean `go run .` dying at startup
// over a feature nothing depends on.
func NotificationsPossible() (bool, string) {
	if runtime.GOOS != "darwin" {
		return true, ""
	}
	exe, err := os.Executable()
	if err != nil {
		return false, "не удалось определить, откуда запущено приложение"
	}
	if strings.Contains(exe, ".app/Contents/MacOS/") {
		return true, ""
	}
	return false, "приложение запущено не из .app — на macOS системные уведомления требуют бандла"
}

// Show puts an open question out as a notification, or takes one back when the
// question has been answered somewhere else.
func (n *Notifier) Show(a acp.Attention) {
	if n == nil || !n.ok || a.QuestionID == "" {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	if !a.Awaiting {
		// Answered on the card or in the panel: the notification is stale, and
		// a stale question is worse than no question.
		if n.shown[a.QuestionID] {
			delete(n.shown, a.QuestionID)
			if err := n.service.RemovePendingNotification(a.QuestionID); err != nil {
				n.app.log.Debug("не удалось убрать уведомление", "err", err)
			}
		}
		return
	}
	if n.shown[a.QuestionID] {
		return
	}

	// The agent's own labels become the buttons: it knows what it is asking
	// better than we do. A free-text answer becomes the reply field.
	actions := make([]notifications.NotificationAction, 0, len(a.Options))
	for _, opt := range a.Options {
		actions = append(actions, notifications.NotificationAction{ID: opt.ID, Title: opt.Label})
	}
	category := notifications.NotificationCategory{
		ID: "question-" + a.QuestionID, Actions: actions,
		HasReplyField: a.FreeText, ReplyPlaceholder: "Ответить своими словами",
		ReplyButtonTitle: "Ответить",
	}
	if err := n.service.RegisterNotificationCategory(category); err != nil {
		n.app.log.Debug("не удалось зарегистрировать вопрос для уведомления", "err", err)
		return
	}

	title := a.CardTitle
	if title == "" {
		title = "Агент спрашивает"
	}
	err := n.service.SendNotificationWithActions(notifications.NotificationOptions{
		ID: a.QuestionID, Title: title, Subtitle: a.Agent, Body: a.Text,
		CategoryID: category.ID,
		// The agent is stopped until this is answered, so it is worth a
		// notification that does not wait for a quiet moment.
		InterruptionLevel: notifications.InterruptionLevelTimeSensitive,
		Data:              map[string]interface{}{"questionId": a.QuestionID, "cardId": a.CardID},
	})
	if err != nil {
		n.app.log.Debug("не удалось показать уведомление", "err", err)
		return
	}
	n.shown[a.QuestionID] = true
}

// onResponse answers the question the way the notification was interacted with.
// Opening the notification is not an answer: it means "let me look", so the
// agent keeps waiting and the card is where the answer is given.
func (n *Notifier) onResponse(result notifications.NotificationResult) {
	if result.Error != nil {
		n.app.log.Debug("ответ на уведомление не прочитан", "err", result.Error)
		return
	}
	id := result.Response.ID
	if id == "" {
		id, _ = result.Response.UserInfo["questionId"].(string)
	}
	if id == "" {
		return
	}
	n.mu.Lock()
	delete(n.shown, id)
	n.mu.Unlock()

	action := result.Response.ActionIdentifier
	text := strings.TrimSpace(result.Response.UserText)
	switch {
	case text != "":
		n.answer(id, acp.Answer{Text: text})
	case action != "" && action != notifications.DefaultActionIdentifier:
		n.answer(id, acp.Answer{OptionID: action})
	default:
		// Just opened. Nothing is decided for a person by their curiosity.
	}
}

func (n *Notifier) answer(questionID string, ans acp.Answer) {
	if err := n.app.Agents.Answer(questionID, ans); err != nil {
		n.app.log.Info("ответ из уведомления не принят", "err", fmt.Errorf("%w", err))
	}
}

// notifyAttention is the hook App.Emit calls: an attention event carries the
// whole question, so nothing has to be looked up to show it.
func (a *App) notifyAttention(payload any) {
	a.uiMu.RLock()
	n := a.notifier
	a.uiMu.RUnlock()
	if n == nil {
		return
	}
	if att, ok := payload.(acp.Attention); ok {
		n.Show(att)
	}
}
